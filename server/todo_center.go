package main

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

func createTodoCenterTables() error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS data_quality_snapshots (
    shop_id       INTEGER NOT NULL DEFAULT 1,
    snapshot_date TEXT NOT NULL,
    score         INTEGER NOT NULL DEFAULT 100,
    issue_count   INTEGER NOT NULL DEFAULT 0,
    affected      INTEGER NOT NULL DEFAULT 0,
    impact        REAL NOT NULL DEFAULT 0,
    checked_count INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL,
    PRIMARY KEY (shop_id, snapshot_date)
);
CREATE INDEX IF NOT EXISTS idx_quality_snapshots_shop_date
    ON data_quality_snapshots(shop_id, snapshot_date DESC);
`)
	return err
}

func saveQualitySnapshot(sid int64, health opsHealth) {
	day := time.Now().Format("2006-01-02")
	ts := nowStr()
	_, _ = db.Exec(`INSERT INTO data_quality_snapshots
        (shop_id,snapshot_date,score,issue_count,affected,impact,checked_count,created_at,updated_at)
        VALUES(?,?,?,?,?,?,?,?,?)
        ON CONFLICT(shop_id,snapshot_date) DO UPDATE SET
        score=excluded.score,issue_count=excluded.issue_count,affected=excluded.affected,
        impact=excluded.impact,checked_count=excluded.checked_count,updated_at=excluded.updated_at`,
		sid, day, health.Score, health.IssueCount, health.Affected,
		health.Impact, health.CheckedCount, ts, ts)
}

type qualityTrendPoint struct {
	Date       string  `json:"date"`
	Score      int     `json:"score"`
	IssueCount int     `json:"issue_count"`
	Affected   int     `json:"affected"`
	Impact     float64 `json:"impact"`
}

func loadQualityTrend(sid int64, days int) []qualityTrendPoint {
	if days <= 0 || days > 365 {
		days = 84
	}
	from := time.Now().AddDate(0, 0, -days+1).Format("2006-01-02")
	rows, err := db.Query(`SELECT snapshot_date,score,issue_count,affected,impact
        FROM data_quality_snapshots WHERE shop_id=? AND snapshot_date>=?
        ORDER BY snapshot_date`, sid, from)
	if err != nil {
		return []qualityTrendPoint{}
	}
	defer rows.Close()
	points := []qualityTrendPoint{}
	for rows.Next() {
		var p qualityTrendPoint
		if rows.Scan(&p.Date, &p.Score, &p.IssueCount, &p.Affected, &p.Impact) == nil {
			p.Impact = round2(p.Impact)
			points = append(points, p)
		}
	}
	return points
}

type todoCenterResult struct {
	Health      opsHealth           `json:"health"`
	Items       []opsIssue          `json:"items"`
	Total       int                 `json:"total"`
	Amount      float64             `json:"amount"`
	Trend       []qualityTrendPoint `json:"trend"`
	LastSync    string              `json:"last_sync"`
	SyncError   string              `json:"sync_error"`
	BackupLast  string              `json:"backup_last"`
	GeneratedAt string              `json:"generated_at"`
}

func handleOperationsTodos(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "方法不允许")
		return
	}
	sid := shopID(r)
	health, err := buildOperationsHealth(sid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取待办数据失败")
		return
	}
	saveQualitySnapshot(sid, health)
	items := append([]opsIssue(nil), health.Issues...)

	syncError := strings.TrimSpace(gfGet(gfKeySyncErr, sid))
	lastSync := strings.TrimSpace(gfGet(gfKeyLast, sid))
	if syncError != "" {
		items = append(items, opsIssue{
			Key: "sync_error", Title: "闲鱼同步需要处理", Severity: "danger",
			Description: syncError, Count: 1, Action: "settings", Filter: "goofish",
			Items: []opsIssueItem{},
		})
	}

	backupLast := strings.TrimSpace(getSetting("backup_last"))
	if isGlobalAdmin(r) && backupEnabled() {
		staleAfter := time.Duration(backupIntervalHours()*2) * time.Hour
		last, parseErr := time.ParseInLocation("2006-01-02 15:04:05", backupLast, time.Local)
		if backupLast == "" || (parseErr == nil && time.Since(last) > staleAfter) {
			desc := "还没有可用的自动备份，建议现在创建一份。"
			if backupLast != "" {
				desc = "上次备份是 " + backupLast + "，已经超过设定的备份间隔。"
			}
			items = append(items, opsIssue{
				Key: "backup_stale", Title: "自动备份已过期", Severity: "warn",
				Description: desc, Count: 1, Action: "settings", Filter: "backup",
				Items: []opsIssueItem{},
			})
		}
	}

	total, amount := 0, 0.0
	for _, item := range items {
		total += item.Count
		amount += item.Amount
	}
	writeJSON(w, todoCenterResult{
		Health: health, Items: items, Total: total, Amount: round2(amount),
		Trend: loadQualityTrend(sid, 84), LastSync: lastSync, SyncError: syncError,
		BackupLast: backupLast, GeneratedAt: nowStr(),
	})
}

func handleOperationsQualityTrend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "方法不允许")
		return
	}
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	writeJSON(w, map[string]interface{}{
		"items": loadQualityTrend(shopID(r), days),
		"days": func() int {
			if days <= 0 || days > 365 {
				return 84
			}
			return days
		}(),
	})
}

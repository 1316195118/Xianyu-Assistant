package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/scrypt"
)

const authCookie = "xy_session"

func authEnabled() bool {
	return getSetting("auth_hash") != ""
}

// secret 用于签发会话 token，首次使用时随机生成并持久化
func secretKey() []byte {
	s := getSetting("auth_secret")
	if s == "" {
		b := make([]byte, 32)
		rand.Read(b)
		s = hex.EncodeToString(b)
		setSetting("auth_secret", s)
	}
	return []byte(s)
}

// hashPasswordLegacy 是历史格式：hex(sha256(salt + "\x00" + pw))，单次哈希。
// 只用于兼容校验老数据，登录成功后会透明升级为 scrypt。
func hashPasswordLegacy(pw, salt string) string {
	h := sha256.New()
	h.Write([]byte(salt + "\x00" + pw))
	return hex.EncodeToString(h.Sum(nil))
}

// scrypt 参数：N=32768 / r=8 / p=1，约 32MB 内存、~50ms/次，抗 GPU 暴力破解。
const (
	scryptN      = 1 << 15
	scryptR      = 8
	scryptP      = 1
	scryptKeyLen = 32
)

// hashPassword 生成 scrypt 哈希，格式 scrypt$N$r$p$salt$hex；salt 为空时随机生成。
func hashPassword(pw, salt string) (string, error) {
	if salt == "" {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		salt = hex.EncodeToString(b)
	}
	dk, err := scrypt.Key([]byte(pw), []byte(salt), scryptN, scryptR, scryptP, scryptKeyLen)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("scrypt$%d$%d$%d$%s$%s", scryptN, scryptR, scryptP, salt, hex.EncodeToString(dk)), nil
}

// verifyPassword 校验密码：scrypt 格式按自身参数校验；旧 sha256 格式用 legacySalt 校验。
func verifyPassword(pw, stored, legacySalt string) bool {
	if stored == "" {
		return false
	}
	if strings.HasPrefix(stored, "scrypt$") {
		parts := strings.Split(stored, "$")
		if len(parts) != 6 {
			return false
		}
		n, e1 := strconv.Atoi(parts[1])
		r, e2 := strconv.Atoi(parts[2])
		p, e3 := strconv.Atoi(parts[3])
		if e1 != nil || e2 != nil || e3 != nil {
			return false
		}
		// 限制参数范围，避免被篡改的超大 N 拖垮 CPU
		if n < 1<<12 || n > 1<<20 || r < 1 || r > 32 || p < 1 || p > 8 {
			return false
		}
		want, err := hex.DecodeString(parts[5])
		if err != nil || len(want) == 0 {
			return false
		}
		dk, err := scrypt.Key([]byte(pw), []byte(parts[4]), n, r, p, len(want))
		if err != nil {
			return false
		}
		return hmac.Equal(dk, want)
	}
	return hmac.Equal([]byte(hashPasswordLegacy(pw, legacySalt)), []byte(stored))
}

// authVersion 会话版本号。任何密码变更都会 +1，
// 使已签发的令牌在下次请求时失效——否则改完密码，旧设备仍能凭旧 cookie 进七天。
func authVersion() string {
	if v := getSetting("auth_ver"); v != "" {
		return v
	}
	return "0"
}

func bumpAuthVersion() {
	n, _ := strconv.Atoi(authVersion())
	_ = setSetting("auth_ver", strconv.Itoa(n+1))
}

// signToken 签发管理员会话（role=admin）；保留原有调用点不变。
func signToken(exp int64) string { return signTokenRole(exp, "admin") }

// signTokenRole 令牌格式 exp.role.hmac，签名覆盖 "exp.role"。
func signTokenRole(exp int64, role string) string {
	payload := strconv.FormatInt(exp, 10) + "." + role
	return payload + "." + tokenSig(payload)
}

// tokenSig 签名时混入会话版本：版本一变，旧令牌的签名就再也对不上。
func tokenSig(payload string) string {
	m := hmac.New(sha256.New, secretKey())
	m.Write([]byte(payload + "." + authVersion()))
	return hex.EncodeToString(m.Sum(nil))
}

// signTokenShop 签发绑定店铺的会话：exp.role.shop.hmac，签名覆盖前两段。
// 店铺写进令牌而不是信任请求头 —— 否则改个 header 就能跨店读取，密码形同虚设。
func signTokenShop(exp int64, role string, shop int64) string {
	payload := strconv.FormatInt(exp, 10) + "." + role + "." + strconv.FormatInt(shop, 10)
	return payload + "." + tokenSig(payload)
}

// verifyTokenRole 校验令牌并返回角色（兼容旧格式，shop 信息丢弃）。
func verifyTokenRole(tok string) (bool, string) {
	ok, role, _ := verifyTokenFull(tok)
	return ok, role
}

// verifyTokenFull 校验令牌，返回角色与绑定店铺（0 表示未绑定）。
// 兼容历史格式：exp.hmac / exp.role.hmac，保证升级后已签发的 cookie 仍有效。
func verifyTokenFull(tok string) (bool, string, int64) {
	parts := strings.Split(tok, ".")
	var expStr, role, sig, signed string
	var shop int64
	switch len(parts) {
	case 2: // 旧格式 exp.hmac
		expStr, role, sig, signed = parts[0], "admin", parts[1], parts[0]
	case 3: // exp.role.hmac
		expStr, role, sig = parts[0], parts[1], parts[2]
		if role != "admin" && role != "readonly" {
			return false, "", 0
		}
		signed = expStr + "." + role
	case 4: // exp.role.shop.hmac
		expStr, role = parts[0], parts[1]
		if role != "admin" && role != "readonly" {
			return false, "", 0
		}
		v, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil || v < 0 {
			return false, "", 0
		}
		shop = v
		sig = parts[3]
		signed = expStr + "." + role + "." + parts[2]
	default:
		return false, "", 0
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || exp < time.Now().Unix() {
		return false, "", 0
	}
	want := tokenSig(signed)
	if !hmac.Equal([]byte(sig), []byte(want)) {
		return false, "", 0
	}
	return true, role, shop
}

// sessionShop 返回会话绑定的店铺 id；未绑定（旧令牌/只读会话）返回 false。
func sessionShop(r *http.Request) (int64, bool) {
	c, err := r.Cookie(authCookie)
	if err != nil {
		return 0, false
	}
	ok, _, shop := verifyTokenFull(c.Value)
	if !ok || shop <= 0 {
		return 0, false
	}
	return shop, true
}

// sessionValidFor 判断当前会话是否就是该店铺的会话。
func sessionValidFor(r *http.Request, shopID int64) bool {
	c, err := r.Cookie(authCookie)
	if err != nil {
		return false
	}
	ok, _, shop := verifyTokenFull(c.Value)
	return ok && shop > 0 && shop == shopID
}

// shopAccessAllowed 判断当前请求能否访问该店铺。
//
//	无密码店铺        —— 放行（保持"自己用"的便利，脚本与直链也不受影响）
//	该店铺的会话      —— 放行
//	全局只读会话      —— 放行（只读账号本来就能看所有店，写操作另有 403 拦截）
//	其它情况          —— 拒绝，前端据此弹出该店密码框
func shopAccessAllowed(r *http.Request, shopID int64) bool {
	if !shopHasPassword(shopID) {
		return true
	}
	if sessionValidFor(r, shopID) {
		return true
	}
	// 全局密码是"总钥匙"：用它登录后可以进任何店铺，不需要再输店密码。
	// 店铺密码用于把某个店单独交给别人（对方只拿店密码，进不了其它店）。
	if isGlobalAdmin(r) {
		return true
	}
	return sessionRole(r) == "readonly"
}

// anyPasswordConfigured 系统里是否设置了任何密码（全局或店铺）。
func anyPasswordConfigured() bool {
	var n int
	// 一条查询判断全局密码和店铺密码，避免每个请求先查 settings 再查 shops。
	err := db.QueryRow(`SELECT CASE WHEN
        EXISTS(SELECT 1 FROM settings WHERE key='auth_hash' AND value<>'')
        OR EXISTS(SELECT 1 FROM shops WHERE COALESCE(password_hash,'')<>'')
        THEN 1 ELSE 0 END`).Scan(&n)
	return err == nil && n > 0
}

// hasAnySession 是否持有任意有效会话（不区分全局/店铺）。
func hasAnySession(r *http.Request) bool {
	c, err := r.Cookie(authCookie)
	if err != nil {
		return false
	}
	ok, _, _ := verifyTokenFull(c.Value)
	return ok
}

// isGlobalAdmin 当前是否是全局密码签发的管理员会话（未绑定店铺）。
func isGlobalAdmin(r *http.Request) bool {
	// 有效会话先验签：店铺会话可直接判为非全局管理员，无需再查密码配置。
	if c, err := r.Cookie(authCookie); err == nil {
		if ok, role, shop := verifyTokenFull(c.Value); ok {
			return role == "admin" && shop == 0
		}
	}
	// 一个密码都没设时，应用本就不设防：不该把新建/删除店铺这类全局操作
	// 反锁住（否则多店铺功能在未设密码的部署里直接不可用）。
	if !anyPasswordConfigured() {
		return true
	}
	return false
}

// shopHasPassword 该店铺是否设了密码。
func shopHasPassword(id int64) bool {
	var h string
	if err := db.QueryRow("SELECT COALESCE(password_hash,'') FROM shops WHERE id=?", id).Scan(&h); err != nil {
		return false
	}
	return h != ""
}

func verifyToken(tok string) bool {
	ok, _ := verifyTokenRole(tok)
	return ok
}

// sessionRole 返回当前会话角色（admin / readonly / 空）。
func sessionRole(r *http.Request) string {
	c, err := r.Cookie(authCookie)
	if err != nil {
		return ""
	}
	if ok, role := verifyTokenRole(c.Value); ok {
		return role
	}
	return ""
}

func isAuthed(r *http.Request) bool {
	if !authEnabled() {
		return true
	}
	c, err := r.Cookie(authCookie)
	if err != nil {
		return false
	}
	return verifyToken(c.Value)
}

// statusCapture 只用于写请求：记录状态码，供「成功后广播变更」判断。
// 同时透传 Flush，避免将来把流式接口挂到写方法上时失效。
type statusCapture struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusCapture) WriteHeader(code int) {
	if !s.wrote {
		s.status = code
		s.wrote = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusCapture) Write(b []byte) (int, error) {
	if !s.wrote {
		s.status = 200
		s.wrote = true
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusCapture) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// clientID 读取前端带的稳定客户端标识，用于让发起方忽略自己触发的变更通知。
func clientID(r *http.Request) string {
	id := r.Header.Get("X-Client-Id")
	if len(id) > 64 {
		id = id[:64]
	}
	return id
}

// authWrap 保护所有业务 API；未启用密码时直接放行。
// readonly 会话对写操作（POST/PUT/DELETE/PATCH，除 /api/auth/* 与 /api/health）返回 403。
// 写操作成功后统一广播 SSE 变更，业务 handler 不需要各自调用。
func authWrap(fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !isAuthed(r) {
			writeError(w, 401, "未登录或登录已过期")
			return
		}
		if isWriteMethod(r.Method) && !readonlyExempt(r.URL.Path) && sessionRole(r) == "readonly" {
			writeError(w, 403, "只读账号不能修改数据")
			return
		}
		// 跨店汇总与全局系统能力只属于全局管理员。店铺会话即使手工构造请求，
		// 也不能借 all=1、更新、备份或系统设置越过本店边界。
		if !isGlobalAdmin(r) {
			if r.URL.Query().Get("all") == "1" {
				writeError(w, 403, "全部店铺汇总需要全局管理员登录")
				return
			}
			if isGlobalOnlyPath(r.URL.Path) {
				writeError(w, 403, "该功能需要全局管理员登录")
				return
			}
		}
		// 店铺上下文统一在这里定好：所有业务查询都依赖它，
		// 分散到各 handler 解析必然会漏，而漏一处就是跨店串数据。
		sid, err := resolveShop(r)
		if err != nil {
			// 店铺列表/管理接口本身不依赖「当前店铺」：删掉当前店铺后，
			// 后续请求仍会带着那个已失效的 id，若这里直接 400，前端连列表都刷不出来，
			// 会卡在「一个店都没有」的死状态。这类接口统一回落到默认店铺。
			if strings.HasPrefix(r.URL.Path, "/api/shops") {
				sid = defaultShopID
			} else {
				writeError(w, 400, err.Error())
				return
			}
		}
		// 整库备份 / 恢复涉及所有店铺的数据，只允许全局密码会话使用。
		// 用店铺密码登录的人即使只该看一家，也不能借备份把整库带走。
		if (strings.HasPrefix(r.URL.Path, "/api/backups") || strings.HasPrefix(r.URL.Path, "/api/backup/")) &&
			anyPasswordConfigured() && !isGlobalAdmin(r) {
			writeError(w, 403, "备份功能需要全局密码登录")
			return
		}
		// 店铺密码：设了密码的店，必须持有「这个店」的会话才能访问。
		// 只读账号（全局只读密码签发、未绑定店铺）保留只读全店的能力。
		if !shopAccessAllowed(r, sid) {
			writeError(w, 401, "需要输入该店铺的密码")
			return
		}
		r = withShop(r, sid)
		if isWriteMethod(r.Method) && shouldBroadcast(r.Method, r.URL.Path) {
			cap := &statusCapture{ResponseWriter: w, status: 200}
			fn(cap, r)
			if cap.status >= 200 && cap.status < 300 {
				realtimeHub.publish(r.Method, r.URL.Path, clientID(r),
					summarizeChange(r.Method, r.URL.Path), sid)
			}
			return
		}
		fn(w, r)
	}
}

func isGlobalOnlyPath(path string) bool {
	switch {
	case path == "/api/settings", path == "/api/settings/test":
		return true
	case strings.HasPrefix(path, "/api/update/"):
		return true
	case strings.HasPrefix(path, "/api/backups"), strings.HasPrefix(path, "/api/backup/"):
		return true
	}
	return false
}

func isWriteMethod(m string) bool {
	switch m {
	case "POST", "PUT", "DELETE", "PATCH":
		return true
	}
	return false
}

func readonlyExempt(p string) bool {
	return strings.HasPrefix(p, "/api/auth/") || p == "/api/health"
}

// handleAuthStatus 登录页的数据源：返回可选店铺与当前会话。
// 该接口不鉴权（否则未登录时连"有哪些店"都拿不到），只暴露名称/颜色/是否有密码。
func handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	all, _ := listShops()
	type pubShop struct {
		ID          int64  `json:"id"`
		Name        string `json:"name"`
		Color       string `json:"color"`
		HasPassword bool   `json:"has_password"`
	}
	out := make([]pubShop, 0, len(all))
	for _, s := range all {
		out = append(out, pubShop{s.ID, s.Name, s.Color, s.HasPassword})
	}
	cur, _ := sessionShop(r)
	// 当前请求上下文里的店铺（api.js 会带 X-Shop-Id）是否需要密码
	needLogin := false
	if sid, err := resolveShop(r); err == nil && shopHasPassword(sid) && !shopAccessAllowed(r, sid) {
		needLogin = true
	}
	writeJSON(w, map[string]interface{}{
		"shops":          out,
		"shop_id":        cur,
		"authenticated":  cur > 0,
		"role":           sessionRole(r),
		"global":         authEnabled(),    // 是否设置了全局密码
		"has_session":    hasAnySession(r), // 任意有效会话（全局或店铺）
		"global_session": isGlobalAdmin(r), // 是否全局密码会话（可自由切换店铺）
		"need_login":     needLogin,
	})
}

// handleShopLogin 店铺登录：POST {shop_id, password}
// 无密码店铺直接放行；有密码则校验后签发绑定该店铺的会话。
func handleShopLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeError(w, 405, "method not allowed")
		return
	}
	if !allowLogin(remoteIP(r)) {
		writeError(w, 429, "尝试过于频繁，请稍后再试")
		return
	}
	var in struct {
		ShopID   int64  `json:"shop_id"`
		Password string `json:"password"`
	}
	if !decodeJSONBody(w, r, &in, maxTinyJSONBody) {
		return
	}
	if in.ShopID <= 0 {
		writeError(w, 400, "请选择店铺")
		return
	}
	var name, hash string
	err := db.QueryRow("SELECT name, COALESCE(password_hash,'') FROM shops WHERE id=? AND archived_at=''",
		in.ShopID).Scan(&name, &hash)
	if err != nil {
		writeError(w, 404, "店铺不存在")
		return
	}
	if hash != "" && !verifyPassword(in.Password, hash, "") {
		writeError(w, 401, "密码错误")
		return
	}
	exp := time.Now().Add(7 * 24 * time.Hour).Unix()
	setSessionCookie(w, r, signTokenShop(exp, "admin", in.ShopID))
	writeJSON(w, map[string]interface{}{"ok": true, "shop_id": in.ShopID, "name": name})
}

func handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeError(w, 405, "method not allowed")
		return
	}
	// 简单内存限流：同 IP 10 次/分钟，超限 429。
	if !allowLogin(remoteIP(r)) {
		writeError(w, 429, "尝试过于频繁，请稍后再试")
		return
	}
	if !authEnabled() {
		writeJSON(w, map[string]interface{}{"ok": true, "no_password": true})
		return
	}
	var in struct {
		Password string `json:"password"`
	}
	if !decodeJSONBody(w, r, &in, maxTinyJSONBody) {
		return
	}
	exp := time.Now().Add(7 * 24 * time.Hour).Unix()
	// 1) 管理员密码
	want := getSetting("auth_hash")
	if verifyPassword(in.Password, want, getSetting("auth_salt")) {
		// 旧 sha256 哈希登录成功后透明升级为 scrypt
		if !strings.HasPrefix(want, "scrypt$") {
			salt := getSetting("auth_salt")
			if nh, err := hashPassword(in.Password, salt); err == nil {
				setSetting("auth_hash", nh)
				setSetting("auth_salt", salt)
			}
		}
		setSessionCookie(w, r, signToken(exp))
		writeJSON(w, map[string]bool{"ok": true})
		return
	}
	// 2) 只读密码（若已配置）：签发 role=readonly 的会话
	if roHash := getSetting("readonly_password_hash"); roHash != "" {
		if verifyPassword(in.Password, roHash, getSetting("readonly_salt")) {
			if !strings.HasPrefix(roHash, "scrypt$") {
				if nh, err := hashPassword(in.Password, getSetting("readonly_salt")); err == nil {
					setSetting("readonly_password_hash", nh)
				}
			}
			setSessionCookie(w, r, signTokenRole(exp, "readonly"))
			writeJSON(w, map[string]bool{"ok": true})
			return
		}
	}
	writeError(w, 401, "密码错误")
}

func requestIsHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if !requestFromTrustedProxy(r) {
		return false
	}
	proto := r.Header.Get("X-Forwarded-Proto")
	if i := strings.IndexByte(proto, ','); i >= 0 {
		proto = proto[:i]
	}
	return strings.EqualFold(strings.TrimSpace(proto), "https")
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, tok string) {
	http.SetCookie(w, &http.Cookie{
		Name: authCookie, Value: tok, Path: "/", MaxAge: 7 * 24 * 3600,
		HttpOnly: true, Secure: requestIsHTTPS(r), SameSite: http.SameSiteLaxMode,
	})
}

func handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeError(w, 405, "method not allowed")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: authCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: requestIsHTTPS(r), SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, map[string]bool{"ok": true})
}

// handleAuthPassword 设置/修改/关闭访问密码。
// 已启用时必须已登录且提供正确的旧密码；未启用时可直接设置。
func handleAuthPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != "PUT" {
		writeError(w, 405, "method not allowed")
		return
	}
	var in struct {
		Old      string `json:"old"`
		Password string `json:"password"`
		Disable  bool   `json:"disable"`
	}
	if !decodeJSONBody(w, r, &in, maxTinyJSONBody) {
		return
	}
	// 只要系统里已有任何密码（全局或店铺），改全局密码就必须由全局管理员会话发起。
	// 否则只给某家店配了密码时，匿名访问者可以抢先把全局密码设成自己的。
	if anyPasswordConfigured() {
		if !hasAnySession(r) {
			writeError(w, 401, "未登录")
			return
		}
		if !isGlobalAdmin(r) {
			writeError(w, 403, "只有全局管理员可以修改访问密码")
			return
		}
		if !verifyPassword(in.Old, getSetting("auth_hash"), getSetting("auth_salt")) {
			writeError(w, 403, "旧密码不正确")
			return
		}
	}
	if in.Disable {
		setSetting("auth_hash", "")
		setSetting("auth_salt", "")
		// 管理密码关掉后，只读密码会变成唯一还能用的旧入口，留着只会让人困惑：
		// 明明「密码已关闭」，却仍能用旧只读密码登录。一并清掉。
		clearReadonlyPassword()
		bumpAuthVersion()
		writeJSON(w, map[string]bool{"ok": true, "readonly_cleared": true})
		return
	}
	if len([]rune(in.Password)) < 8 {
		writeError(w, 400, "密码至少 8 位（建议字母 + 数字）")
		return
	}
	saltBytes := make([]byte, 16)
	rand.Read(saltBytes)
	salt := hex.EncodeToString(saltBytes)
	h, err := hashPassword(in.Password, salt)
	if err != nil {
		writeError(w, 500, "密码哈希失败")
		return
	}
	setSetting("auth_salt", salt)
	if err := setSetting("auth_hash", h); err != nil {
		writeError(w, 500, "保存密码失败")
		return
	}
	bumpAuthVersion()
	// 设置/修改成功后无条件签发新会话，避免刚开启密码就被踢回登录页
	tok := signToken(time.Now().Add(7 * 24 * time.Hour).Unix())
	setSessionCookie(w, r, tok)
	writeJSON(w, map[string]bool{"ok": true})
}

// resetAuthFromCLI 用于忘记密码时本机恢复：xianyu-stat --reset-password
func resetAuthFromCLI() {
	if err := setSetting("auth_hash", ""); err != nil {
		fmt.Println("重置失败:", err)
		return
	}
	setSetting("auth_salt", "")
	bumpAuthVersion()
	// 店铺密码同样要清：忘记店密码时，命令行是唯一自救途径。
	if db != nil {
		if _, err := db.Exec("UPDATE shops SET password_hash=''"); err != nil {
			fmt.Println("店铺密码清除失败:", err)
		}
	}
	fmt.Println("访问密码与所有店铺密码已清除，可直接打开系统重新设置。")
}

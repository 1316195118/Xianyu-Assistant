package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// OCRResult 是识别截图后返回的结构化字段（运费、成本由用户手填）
type OCRResult struct {
	Side          string  `json:"side"`            // buy=买进侧 sell=卖出侧
	Product       string  `json:"product"`         // 商品名称（自动配对键）
	Counterparty  string  `json:"counterparty"`    // 对方昵称
	OrderNo       string  `json:"order_no"`        // 订单编号
	DealTime      string  `json:"deal_time"`       // 交易时间
	ListedPrice   float64 `json:"listed_price"`    // 标价
	Price         float64 `json:"price"`           // 成交价 / 买进实付
	Fee           float64 `json:"fee"`             // 软件服务费(绝对值)
	WuyouFee      float64 `json:"wuyou_fee"`       // 无忧卖类目软件服务费(绝对值)
	NetPrice      float64 `json:"net_price"`       // 到手价（已扣上述两项服务费）
	Shipping      float64 `json:"shipping"`        // 运费金额
	SideBuyScore  int     `json:"side_buy_score"`  // 买进特征得分（调试用）
	SideSellScore int     `json:"side_sell_score"` // 卖出特征得分（调试用）
	AmountGarbled bool    `json:"amount_garbled"`  // 金额疑似被遮挡/识别不全（关键词后紧接 * @ 等杂符），需用户核对
}

// recognizeImage 调用 OpenAI 兼容的视觉大模型，识别闲鱼订单截图
func recognizeImage(data []byte, contentType string) (*OCRResult, error) {
	base, apiKey, model := effectiveOCRConfig()
	if apiKey == "" {
		return nil, fmt.Errorf("未配置识别 API：请在「设置」页填入 API Key，或设置环境变量 OCR_API_KEY。也可直接在弹窗中手动填写。")
	}

	b64 := base64.StdEncoding.EncodeToString(data)

	prompt := `你是一个闲鱼订单截图结构化识别助手。请仔细识别图片中的闲鱼订单，提取以下字段，并以纯 JSON 返回（不要代码块、不要任何解释文字）：
{
  "side": "判定规则：页面出现「卖家昵称」标签=我是买家→填 buy(买进侧)；页面出现「买家昵称」标签=我是卖家→填 sell(卖出侧)。注意 OCR 可能把「卖家昵称」误读成「卖家了昵称」等，按语义判断",
  "product": "仅取商品本身的标题文字；不要包含「闲鱼/评价/分享/感谢/特别好/交易成功/去评价」等 UI 提示、评分标签或按钮文字",
  "counterparty": "对方昵称：取「卖家昵称」(或误读变体如卖家了昵称) 之后紧跟的昵称，或「买家昵称」之后的值；只要昵称本身，不要带「复制/下单时间」等",
  "order_no": "「订单编号」后面那串 15~20 位纯数字；注意区分「支付宝交易号」(位数更长，约28位)，不要填支付宝交易号",
  "deal_time": "交易时间：优先取「下单时间」(买进截图) 或「成交时间」(卖出截图)，格式 YYYY-MM-DD HH:MM:SS，没有则空字符串",
  "listed_price": "商品标题旁显示的标价数字(无则0)",
  "price": "成交价数字(买进截图即实付金额，无则0)",
  "fee": "「软件服务费」这一行的绝对值数字(无则0)。注意：不要包含「无忧卖类目软件服务费」，那是单独一项",
  "wuyou_fee": "「无忧卖类目软件服务费」(或「无忧卖服务费」)这一行的绝对值数字(无则0)，这是卖出订单的平台类目服务费",
  "net_price": "预估到手/预计到手/实际到账金额数字(已扣除软件服务费与无忧卖服务费；买进截图填0)",
  "shipping": "仅填「运费」这一行的金额数字：包邮/免运费填 0；有运费则填对应数字。重要：不要把「支付优惠/红包」等优惠抵扣金额填到这里，它们不是运费"
}
识别要点：
- 卖出订单常见两行扣费：「无忧卖类目软件服务费 -¥7.00」和「软件服务费 -¥11.20」，必须分别填入 wuyou_fee 与 fee，不要合并。
- 「支付优惠」「红包」是优惠/折扣，会让实付减少，绝不是运费，shipping 永远只对应「运费」行。
- 运费行若显示 ¥0.00 或「包邮」，shipping 必须填 0。
- 订单编号与支付宝交易号都在「订单编号」附近，只取订单编号那一串。
注意：只返回 JSON 本身。`

	payload := map[string]interface{}{
		"model": model,
		"messages": []map[string]interface{}{
			{"role": "system", "content": "你是闲鱼订单截图结构化识别工具，只输出 JSON。"},
			{"role": "user", "content": []map[string]interface{}{
				{"type": "text", "text": prompt},
				{"type": "image_url", "image_url": map[string]string{
					"url": "data:" + contentType + ";base64," + b64,
				}},
			}},
		},
		"temperature": 0,
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequest("POST", strings.TrimRight(base, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("调用识别 API 失败: %v", err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("识别 API 返回错误 %d: %s", resp.StatusCode, truncate(string(rb), 500))
	}

	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(rb, &out); err != nil {
		return nil, fmt.Errorf("解析识别结果失败: %v", err)
	}
	if len(out.Choices) == 0 {
		return nil, fmt.Errorf("识别 API 无返回内容")
	}
	r, err := parseOCRJSON(out.Choices[0].Message.Content)
	if err != nil {
		return nil, err
	}
	finalizeSellFees(r)
	return r, nil
}

func parseOCRJSON(s string) (*OCRResult, error) {
	s = strings.TrimSpace(s)
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end < 0 || end <= start {
		return nil, fmt.Errorf("未在识别结果中找到 JSON")
	}
	js := s[start : end+1]
	var r OCRResult
	if err := json.Unmarshal([]byte(js), &r); err != nil {
		return nil, fmt.Errorf("JSON 解析失败: %v", err)
	}
	if r.Side != "buy" {
		r.Side = "sell"
	}
	return &r, nil
}

func truncate(s string, n int) string {
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n]) + "..."
}

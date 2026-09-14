package activator

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// AntigravityActivationURL 是 Antigravity 官方 generateContent 唤醒端点。
// 与 Codex chatgpt.com/backend-api/codex/responses 完全独立，禁止混用。
const AntigravityActivationURL = "https://cloudcode-pa.googleapis.com/v1internal:generateContent"

const (
	antigravityUserAgent   = "antigravity"
	antigravityRequestType = "agent"
	antigravityProjectPref = "projects/"
)

// ErrAntigravityProtocol 表示 Antigravity 协议构建失败（品牌名保留英文，其余中文）。
var ErrAntigravityProtocol = errors.New("Antigravity协议无效")

// antigravityActivationBody 是官方 generateContent 信封。
// 禁止混入 OpenAI messages/choices 或 Codex input/store。
type antigravityActivationBody struct {
	Project     string                  `json:"project"`
	Model       string                  `json:"model"`
	UserAgent   string                  `json:"userAgent"`
	RequestType string                  `json:"requestType"`
	RequestID   string                  `json:"requestId"`
	Request     antigravityInnerRequest `json:"request"`
}

type antigravityInnerRequest struct {
	SessionID string               `json:"sessionId"`
	Contents  []antigravityContent `json:"contents"`
}

type antigravityContent struct {
	Role  string            `json:"role"`
	Parts []antigravityPart `json:"parts"`
}

type antigravityPart struct {
	Text string `json:"text"`
}

// BuildAntigravityProtocol 构建 Antigravity 直连唤醒请求。
// 缺 token / model / project 必须在构建期失败，不得发出 HTTP。
func BuildAntigravityProtocol(material AuthMaterial, model, prompt string) (ProtocolRequest, error) {
	token := strings.TrimSpace(material.AccessToken)
	if token == "" {
		return ProtocolRequest{}, fmt.Errorf("%w：缺少访问令牌", ErrAntigravityProtocol)
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return ProtocolRequest{}, fmt.Errorf("%w：缺少模型名称", ErrAntigravityProtocol)
	}
	projectID, err := normalizeAntigravityProjectID(material.ProjectID)
	if err != nil {
		return ProtocolRequest{}, err
	}

	requestID, err := newAntigravityID()
	if err != nil {
		return ProtocolRequest{}, fmt.Errorf("%w：生成请求标识失败", ErrAntigravityProtocol)
	}
	sessionID, err := newAntigravityID()
	if err != nil {
		return ProtocolRequest{}, fmt.Errorf("%w：生成会话标识失败", ErrAntigravityProtocol)
	}

	body, err := json.Marshal(antigravityActivationBody{
		Project:     projectID,
		Model:       model,
		UserAgent:   antigravityUserAgent,
		RequestType: antigravityRequestType,
		RequestID:   requestID,
		Request: antigravityInnerRequest{
			SessionID: sessionID,
			Contents: []antigravityContent{
				{
					Role:  "user",
					Parts: []antigravityPart{{Text: prompt}},
				},
			},
		},
	})
	if err != nil {
		return ProtocolRequest{}, fmt.Errorf("%w：编码请求失败", ErrAntigravityProtocol)
	}

	headers := map[string][]string{
		"Accept":        {"application/json"},
		"Authorization": {"Bearer " + token},
		"Content-Type":  {"application/json"},
		"User-Agent":    {"antigravity/cli/1.0.8 darwin/arm64"},
	}

	return ProtocolRequest{
		Method:  http.MethodPost,
		URL:     AntigravityActivationURL,
		Headers: headers,
		Body:    body,
	}, nil
}

func normalizeAntigravityProjectID(raw string) (string, error) {
	projectID := strings.TrimSpace(raw)
	if projectID == "" {
		return "", fmt.Errorf("%w：缺少项目", ErrAntigravityProtocol)
	}
	if strings.HasPrefix(projectID, antigravityProjectPref) {
		return projectID, nil
	}
	return antigravityProjectPref + projectID, nil
}

func newAntigravityID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

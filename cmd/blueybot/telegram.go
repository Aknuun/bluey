package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Telegram struct {
	token string
	http  *http.Client
}

func NewTelegram(token string) *Telegram {
	return &Telegram{token: token, http: &http.Client{Timeout: 40 * time.Second}}
}

type tgResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
	ErrorCode   int             `json:"error_code"`
}

func (t *Telegram) call(ctx context.Context, method string, payload any, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.telegram.org/bot"+t.token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	var env tgResponse
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("%s: invalid response: %w", method, err)
	}
	if !env.OK {
		return fmt.Errorf("%s: %s (code %d)", method, env.Description, env.ErrorCode)
	}
	if out != nil && len(env.Result) > 0 {
		return json.Unmarshal(env.Result, out)
	}
	return nil
}

type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type User struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	Username  string `json:"username"`
}

type Message struct {
	MessageID int64  `json:"message_id"`
	Chat      Chat   `json:"chat"`
	From      *User  `json:"from"`
	Text      string `json:"text"`
}

type CallbackQuery struct {
	ID      string   `json:"id"`
	From    User     `json:"from"`
	Message *Message `json:"message"`
	Data    string   `json:"data"`
}

type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
}

type InlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data,omitempty"`
	URL          string `json:"url,omitempty"`
}

type InlineKeyboardMarkup struct {
	InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"`
}

func (t *Telegram) GetMe(ctx context.Context) (User, error) {
	var u User
	err := t.call(ctx, "getMe", map[string]any{}, &u)
	return u, err
}

func (t *Telegram) GetUpdates(ctx context.Context, offset int64) ([]Update, error) {
	payload := map[string]any{
		"offset":          offset,
		"timeout":         30,
		"allowed_updates": []string{"message", "callback_query"},
	}
	var updates []Update
	err := t.call(ctx, "getUpdates", payload, &updates)
	return updates, err
}

func (t *Telegram) SendMessage(ctx context.Context, chatID int64, text string, markup *InlineKeyboardMarkup) error {
	payload := map[string]any{
		"chat_id":                  chatID,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	}
	if markup != nil {
		payload["reply_markup"] = markup
	}
	return t.call(ctx, "sendMessage", payload, nil)
}

func (t *Telegram) AnswerCallback(ctx context.Context, id, text string) error {
	payload := map[string]any{"callback_query_id": id}
	if text != "" {
		payload["text"] = text
	}
	return t.call(ctx, "answerCallbackQuery", payload, nil)
}

func (t *Telegram) SendDocument(ctx context.Context, chatID int64, filename string, data []byte, caption string) error {
	var buf bytes.Buffer
	boundary := "bluey" + fmt.Sprint(time.Now().UnixNano())
	write := func(s string) { buf.WriteString(s + "\r\n") }
	write("--" + boundary)
	write(`Content-Disposition: form-data; name="chat_id"`)
	write(fmt.Sprint(chatID))
	write("--" + boundary)
	write(`Content-Disposition: form-data; name="caption"`)
	write(caption)
	write("--" + boundary)
	write(`Content-Disposition: form-data; name="document"; filename="` + filename + `"`)
	write("Content-Type: text/csv")
	write("")
	buf.Write(data)
	write("--" + boundary + "--")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.telegram.org/bot"+t.token+"/sendDocument", &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	resp, err := t.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var env tgResponse
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return err
	}
	if !env.OK {
		return fmt.Errorf("sendDocument: %s (code %d)", env.Description, env.ErrorCode)
	}
	return nil
}

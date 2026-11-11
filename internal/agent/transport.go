package agent

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"remota.local/remota/internal/protocol"
)

var ErrProtocol = errors.New("invalid server response")

type RemoteError struct {
	Status     int
	Code       string
	RetryAfter time.Duration
}

func (e *RemoteError) Error() string { return "server rejected request: " + e.Code }

type Transport struct {
	base      string
	client    *http.Client
	validator *protocol.Validator
}

func NewTransport(address string, allowInsecure bool) (*Transport, error) {
	u, err := url.Parse(address)
	if err != nil || u.User != nil || u.Host == "" || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || strings.Contains(address, "#") {
		return nil, ErrProtocol
	}
	if port := u.Port(); port != "" {
		if number, err := strconv.ParseUint(port, 10, 16); err != nil || number == 0 {
			return nil, ErrProtocol
		}
	}
	if u.Scheme == "http" {
		ip := net.ParseIP(u.Hostname())
		if (ip == nil || !ip.IsLoopback()) && !allowInsecure {
			return nil, ErrProtocol
		}
	}
	v, err := protocol.NewValidator()
	if err != nil {
		return nil, err
	}
	return &Transport{base: strings.TrimSuffix(u.String(), "/"), validator: v,
		client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func validEpoch(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, b := range s {
		if !(b >= '0' && b <= '9' || b >= 'a' && b <= 'f') {
			return false
		}
	}
	return true
}

func retryAfter(h http.Header) time.Duration {
	value := h.Get("Retry-After")
	if seconds, err := strconv.ParseUint(value, 10, 64); (err == nil || errors.Is(err, strconv.ErrRange)) && seconds > 0 {
		return time.Duration(min(seconds, uint64((1<<63-1)/time.Second))) * time.Second
	}
	if until, err := http.ParseTime(value); err == nil {
		return max(0, time.Until(until))
	}
	return 0
}

func (t *Transport) request(ctx context.Context, method, path, token, epoch string, body any, status int, model string) (map[string]any, string, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, "", ErrProtocol
		}
		reader = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(ctx, method, t.base+"/api/v1/agent"+path, reader)
	if err != nil {
		return nil, "", ErrProtocol
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	r, err := t.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer r.Body.Close()
	gotEpoch := r.Header.Get("X-Remota-Epoch")
	epochs := r.Header.Values("X-Remota-Epoch")
	if len(epochs) == 0 && (r.StatusCode >= 500 || r.StatusCode == 429) {
		return nil, "", &RemoteError{Status: r.StatusCode, Code: "SERVER_UNAVAILABLE", RetryAfter: retryAfter(r.Header)}
	}
	if len(epochs) != 1 || !validEpoch(gotEpoch) || epoch != "" && gotEpoch != epoch {
		return nil, "", ErrProtocol
	}
	if r.StatusCode >= 500 && r.StatusCode != status {
		return nil, gotEpoch, &RemoteError{Status: r.StatusCode, Code: "SERVER_UNAVAILABLE", RetryAfter: retryAfter(r.Header)}
	}
	if ids := r.Header.Values("X-Request-ID"); len(ids) != 1 || strings.TrimSpace(ids[0]) == "" {
		return nil, "", ErrProtocol
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
	if err != nil {
		return nil, "", err
	}
	if len(b) > 1<<20 {
		return nil, "", ErrProtocol
	}
	if r.StatusCode == status && status == http.StatusNoContent {
		if len(b) != 0 {
			return nil, "", ErrProtocol
		}
		return nil, gotEpoch, nil
	}
	ct, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || ct != "application/json" || params["charset"] != "" && !strings.EqualFold(params["charset"], "utf-8") {
		return nil, "", ErrProtocol
	}
	value, err := protocol.Parse(b)
	if err != nil {
		return nil, "", ErrProtocol
	}
	if r.StatusCode != status {
		if t.validator.Validate("Error", value) != nil {
			return nil, "", ErrProtocol
		}
		errValue := value.(map[string]any)["error"].(map[string]any)
		return nil, gotEpoch, &RemoteError{Status: r.StatusCode, Code: errValue["code"].(string), RetryAfter: retryAfter(r.Header)}
	}
	if t.validator.Validate(model, value) != nil {
		return nil, "", ErrProtocol
	}
	m, ok := value.(map[string]any)
	if !ok {
		return nil, "", ErrProtocol
	}
	return m, gotEpoch, nil
}

package portsync

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"github.com/x0lie/pia-tun/internal/log"
)

type qBittorrent struct {
	url    string
	user   string
	pass   string
	client *http.Client
	log    *log.Logger
}

func newQBittorrent(apiURL, user, pass string, logger *log.Logger) *qBittorrent {
	jar, _ := cookiejar.New(nil)
	return &qBittorrent{
		url:  apiURL,
		user: user,
		pass: pass,
		client: &http.Client{
			Timeout: 10 * time.Second,
			Jar:     jar,
		},
		log: logger,
	}
}

func (q *qBittorrent) Name() string { return "qbittorrent" }

func (q *qBittorrent) SyncPort(ctx context.Context, port int) error {
	// Step 1: Login (gets session cookie stored automatically in jar)
	if err := q.login(ctx); err != nil {
		return fmt.Errorf("login: %w", err)
	}

	// Step 2: Set the listening port
	if err := q.setPort(ctx, port); err != nil {
		return fmt.Errorf("set port: %w", err)
	}

	// Step 3: Verify it took effect
	if err := q.verifyPort(ctx, port); err != nil {
		return fmt.Errorf("verify: %w", err)
	}

	return nil
}

func (q *qBittorrent) login(ctx context.Context) error {
	form := url.Values{
		"username": {q.user},
		"password": {q.pass},
	}

	q.log.Debug("Attempting qBittorrent login...")
	req, err := http.NewRequestWithContext(ctx, "POST",
		q.url+"/api/v2/auth/login",
		strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := q.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d, body=%s", resp.StatusCode, string(body))
	}
	q.log.Debug("Login successful (status=%d)", resp.StatusCode)
	return nil
}

func (q *qBittorrent) setPort(ctx context.Context, port int) error {
	form := url.Values{
		"json": {fmt.Sprintf(`{"listen_port":%d}`, port)},
	}

	q.log.Debug("Setting port...")
	q.log.Trace("setPort request body: %s", form.Encode())

	req, err := http.NewRequestWithContext(ctx, "POST",
		q.url+"/api/v2/app/setPreferences",
		strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := q.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d, body=%s", resp.StatusCode, body)
	}
	q.log.Debug("Port set successful (status=%d)", resp.StatusCode)
	return nil
}

func (q *qBittorrent) verifyPort(ctx context.Context, port int) error {
	req, err := http.NewRequestWithContext(ctx, "GET",
		q.url+"/api/v2/app/preferences", nil)
	if err != nil {
		return err
	}

	resp, err := q.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	// Quick check: look for "listen_port":PORT in the response
	expected := fmt.Sprintf(`"listen_port":%d`, port)
	q.log.Trace("verifyPort response (first 50 char): %.50s", string(body))
	q.log.Debug("Checking if qBit port updated...")
	q.log.Trace("looking for: %s", expected)
	if !strings.Contains(string(body), expected) {
		return fmt.Errorf("port not set (expected %d in response)", port)
	}

	return nil
}

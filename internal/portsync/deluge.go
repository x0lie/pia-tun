package portsync

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"regexp"
	"strings"
	"time"

	"github.com/x0lie/pia-tun/internal/log"
)

type deluge struct {
	url    string
	pass   string
	client *http.Client
	log    *log.Logger
}

func newDeluge(apiURL, pass string, logger *log.Logger) *deluge {
	jar, _ := cookiejar.New(nil)
	return &deluge{
		url:  apiURL,
		pass: pass,
		client: &http.Client{
			Timeout: 10 * time.Second,
			Jar:     jar,
		},
		log: logger,
	}
}

func (d *deluge) Name() string { return "deluge" }

func (d *deluge) SyncPort(ctx context.Context, port int) error {
	// Step 1: Login
	if err := d.login(ctx); err != nil {
		return fmt.Errorf("login: %w", err)
	}

	// Step 2: Ensure web UI is connected to daemon
	if err := d.ensureConnected(ctx); err != nil {
		return fmt.Errorf("ensure connected: %w", err)
	}

	// Step 3: Set the listen ports
	if err := d.setPort(ctx, port); err != nil {
		return fmt.Errorf("set port: %w", err)
	}

	return nil
}

func (d *deluge) rpc(ctx context.Context, body string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "POST",
		d.url+"/json",
		strings.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := d.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		return string(respBody), fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	return string(respBody), nil
}

func (d *deluge) login(ctx context.Context) error {
	escaped := jsonEscape(d.pass)
	body := fmt.Sprintf(`{"method":"auth.login","params":["%s"],"id":1}`, escaped)

	resp, err := d.rpc(ctx, body)
	if err != nil {
		return err
	}

	d.log.Debug("login response: %.200s", resp)

	if !strings.Contains(resp, `"result"`) || strings.Contains(resp, `"result": false`) {
		return fmt.Errorf("login failed")
	}

	return nil
}

func (d *deluge) ensureConnected(ctx context.Context) error {
	// Check if web UI is connected to daemon
	resp, err := d.rpc(ctx, `{"method":"web.connected","params":[],"id":2}`)
	if err != nil {
		return err
	}

	d.log.Debug("connected check: %.200s", resp)

	// Already connected
	if strings.Contains(resp, `"result": true`) || strings.Contains(resp, `"result":true`) {
		d.log.Debug("web UI already connected to daemon")
		return nil
	}

	// Not connected — get available hosts and connect to first one
	d.log.Debug("web UI not connected, attempting to connect to daemon")

	hostsResp, err := d.rpc(ctx, `{"method":"web.get_hosts","params":[],"id":3}`)
	if err != nil {
		return fmt.Errorf("get hosts: %w", err)
	}

	d.log.Debug("hosts response: %.200s", hostsResp)

	// Extract first host ID (32-character hex string)
	re := regexp.MustCompile(`"([a-f0-9]{32})"`)
	match := re.FindStringSubmatch(hostsResp)
	if match == nil {
		return fmt.Errorf("no daemon hosts found")
	}

	hostID := match[1]
	d.log.Debug("connecting to daemon host: %s", hostID)

	connectBody := fmt.Sprintf(`{"method":"web.connect","params":["%s"],"id":4}`, hostID)
	connectResp, err := d.rpc(ctx, connectBody)
	if err != nil {
		return fmt.Errorf("connect to daemon: %w", err)
	}

	d.log.Debug("connect response: %.200s", connectResp)

	// Wait for connection to establish
	time.Sleep(1 * time.Second)

	return nil
}

func (d *deluge) setPort(ctx context.Context, port int) error {
	body := fmt.Sprintf(`{"method":"core.set_config","params":[{"listen_ports":[%d,%d]}],"id":5}`, port, port)

	resp, err := d.rpc(ctx, body)
	if err != nil {
		return err
	}

	d.log.Debug("set_config response: %.200s", resp)

	if strings.Contains(resp, `"error"`) && !strings.Contains(resp, `"error": null`) && !strings.Contains(resp, `"error":null`) {
		return fmt.Errorf("error in response: %s", resp)
	}

	return nil
}

// jsonEscape escapes a string for use in JSON values.
func jsonEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	s = strings.ReplaceAll(s, "\r", `\r`)
	s = strings.ReplaceAll(s, "\t", `\t`)
	return s
}

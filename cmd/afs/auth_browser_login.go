package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/rowantrollope/afs/internal/controlplane"
)

type browserLoginClient interface {
	StartCLILogin(context.Context, string, string) (controlplane.CLILoginStart, error)
	PollCLILogin(context.Context, string, string) (controlplane.CLILoginPoll, error)
}

var cliRequestIDPattern = regexp.MustCompile(`^cli_[a-f0-9]{32}$`)
var cliUserCodePattern = regexp.MustCompile(`^[A-Z0-9]{4}-[A-Z0-9]{4}$`)

func browserLogin(endpoint, name string, noBrowser bool, output io.Writer, openBrowser func(string) error) (string, error) {
	if _, err := browserApprovalURL(endpoint, "cli_00000000000000000000000000000000"); err != nil {
		return "", err
	}
	remote, err := controlplane.NewCLIClient(endpoint, "")
	if err != nil {
		return "", err
	}
	defer remote.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	token, err := runBrowserLogin(ctx, remote, endpoint, name, noBrowser, output, openBrowser)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return "", errors.New("browser sign-in canceled; your saved connection was not changed")
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return "", errors.New("browser sign-in expired; run afs auth login again")
		}
		return "", err
	}
	verified, err := controlplane.NewCLIClient(endpoint, token)
	if err != nil {
		return "", err
	}
	defer verified.Close()
	verifyCtx, verifyCancel := context.WithTimeout(ctx, 10*time.Second)
	defer verifyCancel()
	if err := verified.VerifyAuthentication(verifyCtx); err != nil {
		return "", errors.New("the issued CLI key could not be verified; your saved connection was not changed")
	}
	return token, nil
}

// Keep approval on the explicitly selected server. A server response must never
// redirect the user (or the device proof) to an unrelated origin.
func browserApprovalURL(endpoint, id string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !cliRequestIDPattern.MatchString(id) {
		return "", errors.New("invalid control-plane browser sign-in URL")
	}
	loopback := strings.EqualFold(u.Hostname(), "localhost")
	if ip := net.ParseIP(u.Hostname()); ip != nil {
		loopback = ip.IsLoopback()
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return "", errors.New("browser sign-in requires HTTPS (HTTP is supported only on localhost)")
	}
	u.Path, u.RawPath, u.RawQuery = "/connect-cli", "", "request="+id
	return u.String(), nil
}

func runBrowserLogin(ctx context.Context, remote browserLoginClient, endpoint, name string, noBrowser bool, output io.Writer, openBrowser func(string) error) (string, error) {
	if name == "" {
		hostname, _ := os.Hostname()
		if hostname == "" {
			hostname = "this computer"
		}
		name = "AFS CLI on " + hostname
	}
	var entropy [32]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", err
	}
	verifier := base64.RawURLEncoding.EncodeToString(entropy[:])
	digest := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(digest[:])
	startCtx, startCancel := context.WithTimeout(ctx, 10*time.Second)
	start, err := remote.StartCLILogin(startCtx, name, challenge)
	startCancel()
	if err != nil {
		return "", err
	}
	approvalURL, err := browserApprovalURL(endpoint, start.ID)
	if err != nil {
		return "", err
	}
	expires, err := time.Parse(time.RFC3339Nano, start.ExpiresAt)
	if err != nil || !expires.After(time.Now()) || !cliUserCodePattern.MatchString(start.UserCode) || start.DeviceCode == "" || start.VerificationPath != "/connect-cli?request="+start.ID {
		return "", errors.New("the control plane returned an invalid browser sign-in request")
	}
	ctx, cancel := context.WithDeadline(ctx, expires)
	defer cancel()
	fmt.Fprintf(output, "\nSign in to AFS in your browser:\n  %s\n\nConfirm this code matches: %s\n", approvalURL, start.UserCode)
	if !noBrowser {
		if err := openBrowser(approvalURL); err != nil {
			fmt.Fprintln(output, "Could not open a browser automatically. Open the link above on any device.")
		}
	}
	fmt.Fprintln(output, "Waiting for approval… (Ctrl+C to cancel)")
	interval := browserPollInterval(start.Interval)
	consecutiveErrors := 0
	for {
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", ctx.Err()
		case <-timer.C:
		}
		pollCtx, pollCancel := context.WithTimeout(ctx, 15*time.Second)
		result, err := remote.PollCLILogin(pollCtx, start.DeviceCode, verifier)
		pollCancel()
		if err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			consecutiveErrors++
			if consecutiveErrors < 4 {
				if consecutiveErrors == 1 {
					fmt.Fprintln(output, "Connection interrupted. Retrying while you approve…")
				}
				interval = time.Duration(1<<consecutiveErrors) * time.Second
				continue
			}
			return "", err
		}
		consecutiveErrors = 0
		switch result.Status {
		case "pending":
			interval = browserPollInterval(result.Interval)
		case "complete":
			if result.Token == "" {
				return "", errors.New("the control plane returned an empty CLI key")
			}
			fmt.Fprintln(output, "Approved. Saving your CLI connection.")
			return result.Token, nil
		case "denied":
			return "", errors.New("browser sign-in was declined or is no longer valid; your saved connection was not changed")
		case "expired":
			return "", errors.New("browser sign-in expired; run afs auth login again")
		default:
			return "", errors.New("the control plane returned an invalid browser sign-in status")
		}
	}
}

func browserPollInterval(seconds int) time.Duration {
	if seconds < 1 {
		seconds = 2
	}
	if seconds > 30 {
		seconds = 30
	}
	return time.Duration(seconds) * time.Second
}

func openAuthBrowser(address string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.CommandContext(ctx, "open", address)
	case "windows":
		command = exec.CommandContext(ctx, "rundll32", "url.dll,FileProtocolHandler", address)
	default:
		command = exec.CommandContext(ctx, "xdg-open", address)
	}
	return command.Run()
}

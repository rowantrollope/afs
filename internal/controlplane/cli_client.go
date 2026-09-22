package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/rowantrollope/afs/internal/filehistory"
	afsclient "github.com/rowantrollope/afs/mount/client"
)

// CLIClient is the authenticated management client. It never follows redirects
// (which could redirect credentials) and never includes transport URLs in errors.
type CLIClient struct {
	baseURL, token string
	http           *http.Client
}

func NewCLIClient(baseURL, token string) (*CLIClient, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("control-plane URL must be an HTTP(S) address without credentials, query, or fragment")
	}
	if strings.ContainsAny(token, "\r\n") {
		return nil, errors.New("invalid control-plane token")
	}
	return &CLIClient{baseURL: strings.TrimRight(u.String(), "/"), token: token, http: &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				conn, err := (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext(ctx, network, address)
				if err != nil {
					return nil, err
				}
				return &idleDeadlineConn{Conn: conn, idle: cliHTTPIdleTimeout}, nil
			},
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 2 * time.Minute,
			IdleConnTimeout:       90 * time.Second,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

// Close releases pooled connections without interrupting in-flight requests.
func (c *CLIClient) Close() error { c.http.CloseIdleConnections(); return nil }

// VerifyAuthentication checks the management identity without requesting Redis
// credentials or requiring a configured, reachable workspace database.
func (c *CLIClient) VerifyAuthentication(ctx context.Context) error {
	var result struct {
		Authenticated bool `json:"authenticated"`
	}
	if err := c.request(ctx, http.MethodGet, "/v1/auth/verify", "", nil, &result); err != nil {
		return err
	}
	if !result.Authenticated {
		return errors.New("control plane did not verify authentication")
	}
	return nil
}

func (c *CLIClient) Connection(ctx context.Context) (ConnectionInfo, error) {
	var result ConnectionInfo
	err := c.request(ctx, http.MethodGet, "/v1/connection", "", nil, &result)
	if err == nil && result.RedisURL == "" {
		err = errors.New("control plane returned no Redis connection")
	}
	return result, err
}

// DatabaseURL pins subsequent management to the database that supplied mount
// credentials. An older server may omit the ID; preserve its existing URL.
func (c *CLIClient) DatabaseURL(databaseID string) (string, error) {
	if databaseID == "" {
		return c.baseURL, nil
	}
	if !validDatabaseID(databaseID) {
		return "", errors.New("control plane returned an invalid database identity")
	}
	u, _ := url.Parse(c.baseURL) // NewCLIClient already validated the URL.
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) >= 2 && parts[len(parts)-2] == "databases" {
		if parts[len(parts)-1] != databaseID {
			return "", errors.New("control plane returned a different database identity")
		}
		return c.baseURL, nil
	}
	return c.baseURL + "/databases/" + databaseID, nil
}

func (c *CLIClient) request(ctx context.Context, method, path, contentType string, body io.Reader, result any) error {
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return errors.New("cannot construct control-plane request")
	}
	if c.token != "" {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response, err := c.http.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("cannot connect to control plane; check its URL and availability")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		var problem struct {
			Error string `json:"error"`
			Code  string `json:"code"`
		}
		_ = json.NewDecoder(io.LimitReader(response.Body, 8192)).Decode(&problem)
		if response.StatusCode == http.StatusUnauthorized {
			return errors.New("control-plane authentication required; check controlPlane.token")
		}
		if response.StatusCode >= 300 && response.StatusCode < 400 {
			return errors.New("control-plane redirects are not allowed")
		}
		message := problem.Error
		if message == "" {
			message = http.StatusText(response.StatusCode)
		}
		if c.token != "" {
			message = strings.ReplaceAll(message, c.token, "[redacted]")
		}
		var cause error
		switch problem.Code {
		case "not_found":
			cause = os.ErrNotExist
		case "import_in_progress":
			cause = ErrImportInProgress
		case "workspace_changed":
			cause = afsclient.ErrWorkspaceChanged
		case "write_conflict":
			cause = afsclient.ErrWriteConflict
		case "conflict":
			cause = ErrWorkspaceConflict
		}
		if cause != nil {
			return fmt.Errorf("control plane: %s: %w", message, cause)
		}
		return fmt.Errorf("control plane: %s", message)
	}
	if result == nil {
		_, err = io.Copy(io.Discard, response.Body)
		return err
	}
	if err = json.NewDecoder(response.Body).Decode(result); err != nil {
		return errors.New("invalid control-plane response")
	}
	return nil
}
func cliCall[T any](ctx context.Context, c *CLIClient, request cliRequest) (T, error) {
	var result T
	body, err := json.Marshal(request)
	if err != nil {
		return result, err
	}
	err = c.request(ctx, http.MethodPost, "/v1/cli", "application/json", bytes.NewReader(body), &result)
	return result, err
}

func (c *CLIClient) ListWorkspaces(ctx context.Context) ([]WorkspaceMeta, error) {
	return cliCall[[]WorkspaceMeta](ctx, c, cliRequest{Operation: "workspace.list"})
}

func (c *CLIClient) GetWorkspace(ctx context.Context, workspace string) (WorkspaceMeta, error) {
	return cliCall[WorkspaceMeta](ctx, c, cliRequest{Operation: "workspace.get", Workspace: workspace})
}

func (c *CLIClient) CreateWorkspace(ctx context.Context, name string) (WorkspaceMeta, error) {
	return cliCall[WorkspaceMeta](ctx, c, cliRequest{Operation: "workspace.create", Name: name})
}

func (c *CLIClient) UpdateWorkspaceSettings(ctx context.Context, workspace, name, description string) (WorkspaceMeta, error) {
	return cliCall[WorkspaceMeta](ctx, c, cliRequest{Operation: "workspace.update", Workspace: workspace, Name: name, Description: description})
}

func (c *CLIClient) WorkspaceGeneration(ctx context.Context, workspace string) (string, error) {
	return cliCall[string](ctx, c, cliRequest{Operation: "workspace.generation", Workspace: workspace})
}

func (c *CLIClient) ListCheckpoints(ctx context.Context, workspace string) ([]SavepointMeta, error) {
	return cliCall[[]SavepointMeta](ctx, c, cliRequest{Operation: "checkpoint.list", Workspace: workspace})
}

func (c *CLIClient) SaveCheckpointFromLiveWithOptions(ctx context.Context, workspace, name string, options CheckpointOptions) (SavepointMeta, error) {
	return cliCall[SavepointMeta](ctx, c, cliRequest{Operation: "checkpoint.save", Workspace: workspace, Name: name, CheckpointOptions: options})
}

func (c *CLIClient) RestoreCheckpoint(ctx context.Context, workspace, ref string) (RestoreCheckpointResult, error) {
	return cliCall[RestoreCheckpointResult](ctx, c, cliRequest{Operation: "checkpoint.restore", Workspace: workspace, Ref: ref})
}

func (c *CLIClient) GetFileHistoryPage(ctx context.Context, workspace string, request FileHistoryRequest) (FileHistoryResponse, error) {
	return cliCall[FileHistoryResponse](ctx, c, cliRequest{Operation: "history.list", Workspace: workspace, History: request})
}

func (c *CLIClient) GetFileContent(ctx context.Context, workspace, view, path string) (FileVersionContentResponse, error) {
	return cliCall[FileVersionContentResponse](ctx, c, cliRequest{Operation: "history.content", Workspace: workspace, Ref: view, Path: path})
}

func (c *CLIClient) GetFileVersionContent(ctx context.Context, workspace, versionID string) (FileVersionContentResponse, error) {
	return cliCall[FileVersionContentResponse](ctx, c, cliRequest{Operation: "history.version", Workspace: workspace, Ref: versionID})
}

func (c *CLIClient) GetFileVersionContentAtOrdinal(ctx context.Context, workspace, fileID string, ordinal int64) (FileVersionContentResponse, error) {
	return cliCall[FileVersionContentResponse](ctx, c, cliRequest{Operation: "history.ordinal", Workspace: workspace, FileID: fileID, Ordinal: ordinal})
}

func (c *CLIClient) DiffFileVersions(ctx context.Context, workspace, path string, from, to FileVersionDiffOperand) (FileVersionDiffResponse, error) {
	return cliCall[FileVersionDiffResponse](ctx, c, cliRequest{Operation: "history.diff", Workspace: workspace, Path: path, From: from, To: to})
}

func (c *CLIClient) RestoreFileVersion(ctx context.Context, workspace, path string, selector FileVersionSelector) (FileVersionRestoreResponse, error) {
	return cliCall[FileVersionRestoreResponse](ctx, c, cliRequest{Operation: "history.restore", Workspace: workspace, Path: path, Selector: selector})
}

func (c *CLIClient) UndeleteFileVersion(ctx context.Context, workspace, path string, selector FileVersionSelector) (FileVersionUndeleteResponse, error) {
	return cliCall[FileVersionUndeleteResponse](ctx, c, cliRequest{Operation: "history.undelete", Workspace: workspace, Path: path, Selector: selector})
}

func (c *CLIClient) GetFileHistoryPolicy(ctx context.Context, workspace string) (filehistory.Policy, error) {
	return cliCall[filehistory.Policy](ctx, c, cliRequest{Operation: "history.policy", Workspace: workspace})
}

func (c *CLIClient) UpdateFileHistoryPolicy(ctx context.Context, workspace string, patch FileHistoryPolicyPatch) (filehistory.Policy, error) {
	return cliCall[filehistory.Policy](ctx, c, cliRequest{Operation: "history.policy.update", Workspace: workspace, PolicyPatch: patch})
}

func (c *CLIClient) PruneFileHistory(ctx context.Context, workspace string, limit int) (filehistory.PruneResult, error) {
	return cliCall[filehistory.PruneResult](ctx, c, cliRequest{Operation: "history.prune", Workspace: workspace, Limit: limit})
}

func (c *CLIClient) DeleteWorkspace(ctx context.Context, workspace string) error {
	_, err := cliCall[any](ctx, c, cliRequest{Operation: "workspace.delete", Workspace: workspace})
	return err
}

func (c *CLIClient) ForkWorkspace(ctx context.Context, source, name, ref string) error {
	_, err := cliCall[any](ctx, c, cliRequest{Operation: "workspace.fork", Workspace: source, Name: name, Ref: ref})
	return err
}

func (c *CLIClient) DeleteCheckpoint(ctx context.Context, workspace, ref string) error {
	_, err := cliCall[any](ctx, c, cliRequest{Operation: "checkpoint.delete", Workspace: workspace, Ref: ref})
	return err
}

func (c *CLIClient) GetCheckpoint(ctx context.Context, workspace, ref string) (SavepointMeta, Manifest, error) {
	result, err := cliCall[cliCheckpoint](ctx, c, cliRequest{Operation: "checkpoint.get", Workspace: workspace, Ref: ref})
	return result.Meta, result.Manifest, err
}
func (c *CLIClient) GetFileHistoryExport(ctx context.Context, workspace, path, selector, fileID string) (filehistory.Record, []byte, error) {
	result, err := cliCall[cliExport](ctx, c, cliRequest{Operation: "history.export", Workspace: workspace, Path: path, Ref: selector, FileID: fileID})
	return result.Record, result.Data, err
}
func (c *CLIClient) SaveCheckpointFromLive(ctx context.Context, workspace, name string) (SavepointMeta, error) {
	return c.SaveCheckpointFromLiveWithOptions(ctx, workspace, name, CheckpointOptions{})
}

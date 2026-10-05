package mcpfiles

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/sys/unix"
)

type FileInput struct {
	Path string `json:"path" jsonschema:"Relative file path, with no traversal or symlinks"`
}
type ListInput struct {
	Path string `json:"path,omitempty" jsonschema:"Relative directory; empty means workspace root"`
}
type WriteInput struct {
	Path    string `json:"path"`
	Content string `json:"content" jsonschema:"UTF-8 content, at most 1048576 bytes"`
	SHA256  string `json:"sha256" jsonschema:"Lowercase SHA-256 hex digest of content; required for safe retries"`
}

func validContent(content string) bool { return utf8.ValidString(content) }

type ToolError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}
type Result struct {
	Workspace string     `json:"workspace"`
	Ready     bool       `json:"ready"`
	Path      string     `json:"path,omitempty"`
	Content   *string    `json:"content,omitempty"`
	SHA256    string     `json:"sha256,omitempty"`
	Bytes     *int       `json:"bytes,omitempty"`
	Created   *bool      `json:"created,omitempty"`
	Verified  bool       `json:"verified"`
	Files     *[]Entry   `json:"files,omitempty"`
	Error     *ToolError `json:"error,omitempty"`
}

func failure(workspace, code, message string, retryable bool) (*mcp.CallToolResult, Result, error) {
	out := Result{Workspace: workspace, Error: &ToolError{Code: code, Message: message, Retryable: retryable}}
	data, _ := json.Marshal(out)
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}}, out, nil
}

func (f *Folder) fileFailure(err error) (*mcp.CallToolResult, Result, error) {
	var rule *fileRuleError
	if errors.As(err, &rule) {
		return failure(f.workspace, rule.code, rule.message, false)
	}
	code, message := "file_error", "file unavailable, unsupported, or unsafe"
	if errors.Is(err, os.ErrNotExist) {
		code, message = "not_found", "file or parent directory not found"
	}
	if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
		code, message = "unsafe_path", "symlinks and non-directory parents are forbidden"
	}
	return failure(f.workspace, code, message, false)
}

func (f *Folder) Server() *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "afs-mcp", Version: "0.1.0"}, &mcp.ServerOptions{Instructions: "One server-managed AFS workspace. Use relative paths. Writes are immutable create-only: always supply the SHA-256 of UTF-8 content and retry identical arguments after an unconfirmed publication. Read/list observe the local synchronized folder. No workspace administration tools."})
	closed := false
	ro := func() *mcp.ToolAnnotations { return &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed} }
	mcp.AddTool(s, &mcp.Tool{Name: "workspace_status", Description: "Check that the configured folder-sync mount is healthy; does not force synchronization or promise delivery to other mounts.", Annotations: ro()}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, Result, error) {
		if err := f.Check(ctx); err != nil {
			return failure(f.workspace, "not_ready", "configured workspace unavailable; operator must check AFS", true)
		}
		return nil, Result{Workspace: f.workspace, Ready: true}, nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "list_files", Description: "List one directory, sorted by relative path, up to 1000 entries. Omits control files, symlinks, hard links and special files. File sizes include unsupported non-UTF-8 or large files; read_file enforces limits.", Annotations: ro()}, func(ctx context.Context, _ *mcp.CallToolRequest, in ListInput) (*mcp.CallToolResult, Result, error) {
		if err := validatePath(in.Path, true); err != nil {
			return failure(f.workspace, "invalid_path", err.Error(), false)
		}
		if err := f.Check(ctx); err != nil {
			return failure(f.workspace, "not_ready", "configured workspace unavailable; operator must check AFS", true)
		}
		entries, err := f.List(in.Path)
		if err != nil {
			return f.fileFailure(err)
		}
		return nil, Result{Workspace: f.workspace, Ready: true, Path: in.Path, Files: &entries}, nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "read_file", Description: "Read a regular UTF-8 file up to 1 MiB, returning its content, byte length and SHA-256 reference.", Annotations: ro()}, func(ctx context.Context, _ *mcp.CallToolRequest, in FileInput) (*mcp.CallToolResult, Result, error) {
		if err := validatePath(in.Path, false); err != nil {
			return failure(f.workspace, "invalid_path", err.Error(), false)
		}
		if err := f.Check(ctx); err != nil {
			return failure(f.workspace, "not_ready", "configured workspace unavailable; operator must check AFS", true)
		}
		data, err := f.Read(in.Path)
		if err != nil {
			return f.fileFailure(err)
		}
		content, n := string(data), len(data)
		return nil, Result{Workspace: f.workspace, Ready: true, Path: in.Path, Content: &content, SHA256: digest(data), Bytes: &n}, nil
	})
	// Verification uses AFS sync --wait, which may publish pending non-MCP
	// changes, including deletions. Keep the conservative destructive hint.
	destructive := true
	mcp.AddTool(s, &mcp.Tool{Name: "write_file", Description: "Create a UTF-8 file up to 1 MiB, creating parents. Never overwrite. Identical existing bytes are a safe retry. Success follows AFS Redis verification; verification can publish other pending folder changes. No Redis disk-durability or other-mount delivery guarantee.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &destructive, IdempotentHint: true, OpenWorldHint: &closed}}, func(ctx context.Context, _ *mcp.CallToolRequest, in WriteInput) (*mcp.CallToolResult, Result, error) {
		if err := validatePath(in.Path, false); err != nil {
			return failure(f.workspace, "invalid_path", err.Error(), false)
		}
		if len(in.Content) > MaxFileBytes || !validContent(in.Content) {
			return failure(f.workspace, "invalid_content", "content must be UTF-8 and at most 1 MiB", false)
		}
		if in.SHA256 != digest([]byte(in.Content)) {
			return failure(f.workspace, "hash_mismatch", "sha256 must match the lowercase SHA-256 of content", false)
		}
		if err := f.Check(ctx); err != nil {
			return failure(f.workspace, "not_ready", "configured workspace unavailable; operator must check AFS", true)
		}
		created, err := f.Write(ctx, in.Path, []byte(in.Content), in.SHA256)
		if err != nil {
			if created {
				return failure(f.workspace, "publication_unconfirmed", "file created but publication unconfirmed; retry identical arguments", true)
			}
			// Re-verify identical retries, even after a prior timeout.
			if data, readErr := f.Read(in.Path); readErr == nil {
				if digest(data) != in.SHA256 {
					return failure(f.workspace, "already_exists", "file exists with different content; choose a new path", false)
				}
				return failure(f.workspace, "publication_unconfirmed", "identical file exists but publication unconfirmed; retry identical arguments", true)
			}
			if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
				return f.fileFailure(err)
			}
			var rule *fileRuleError
			if errors.As(err, &rule) {
				return f.fileFailure(err)
			}
			return failure(f.workspace, "write_failed", "workspace unavailable or path unsafe; operator must check AFS before retrying", true)
		}
		n := len(in.Content)
		return nil, Result{Workspace: f.workspace, Ready: true, Path: in.Path, SHA256: in.SHA256, Bytes: &n, Created: &created, Verified: true}, nil
	})
	return s
}

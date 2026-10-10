package snapshots

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Rclone is a Remote reached with the rclone binary — Google Drive, through a
// crypt remote, in the deployment this was written for. Where copies go is a
// matter of rclone config, not of this code.
type Rclone struct {
	// Target is "remote:path", e.g. "gdrive-crypt:core-store".
	Target string
	// Config is a writable rclone config; rclone rewrites it when it
	// refreshes its Drive token.
	Config string
}

// WriteConfig decodes a base64 rclone config and writes it where rclone can
// update it, returning the path. The config comes through the environment,
// not a mounted file: a deploy's secret file is deleted when the deploy step
// ends, and a container restarted later would find its mount gone. It holds
// the Drive token and the crypt password, hence 0600.
func WriteConfig(b64, dir string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return "", fmt.Errorf("rclone config is not base64: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "rclone.conf")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func (r *Rclone) String() string { return r.Target }

func (r *Rclone) dest(name string) string { return strings.TrimRight(r.Target, "/") + "/" + name }

func (r *Rclone) run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "rclone", append([]string{"--config", r.Config}, args...)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		// rclone's last line says what is wrong — an expired token, a
		// missing folder — and holds no secret.
		msg := strings.TrimSpace(errb.String())
		if i := strings.LastIndex(msg, "\n"); i >= 0 {
			msg = msg[i+1:]
		}
		return nil, fmt.Errorf("%w: %s", err, msg)
	}
	return out.Bytes(), nil
}

// Copy uploads one file under name.
func (r *Rclone) Copy(ctx context.Context, localPath, name string) error {
	_, err := r.run(ctx, "copyto", localPath, r.dest(name))
	return err
}

// List returns the file names at the target.
func (r *Rclone) List(ctx context.Context) ([]string, error) {
	out, err := r.run(ctx, "lsf", "--files-only", r.Target)
	if err != nil {
		// A target nothing has been copied to yet does not exist — rclone
		// creates folders on the first copy — and is empty, not broken.
		// Reported as an error, a fresh deploy would show "copy failed" on
		// the dashboard until its first copy.
		if strings.Contains(err.Error(), "directory not found") {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			names = append(names, l)
		}
	}
	return names, nil
}

// Delete removes one file from the target.
func (r *Rclone) Delete(ctx context.Context, name string) error {
	_, err := r.run(ctx, "deletefile", r.dest(name))
	return err
}

package review

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Gaurav-Gosain/tuios/internal/arc"
)

// ApplyArcToIndex writes only the Arc index. A hunk is applied exactly to
// arc show's index content, then sent to arc add -F -, never to the worktree.
func ApplyArcToIndex(ctx context.Context, root string, f *File, patch string, unstage, whole bool) error {
	if whole {
		if unstage {
			_, err := arc.Run(ctx, root, "reset", "HEAD", "--", f.Path)
			return err
		}
		_, err := arc.Run(ctx, root, "add", "--", f.Path)
		return err
	}
	for _, mode := range []string{f.oldMode, f.newMode} {
		if mode != "" && mode != "000000" && mode != "100644" && mode != "100755" {
			return errors.New("Arc stages symlinks and special files only whole")
		}
	}
	if st, err := os.Lstat(filepath.Join(root, f.Path)); err == nil && !st.Mode().IsRegular() {
		return errors.New("Arc stages symlinks and special files only whole")
	}
	before := ""
	var err error
	if !f.indexLacks(unstage) {
		before, err = arc.Run(ctx, root, "show", ":"+f.Path)
		if err != nil {
			return err
		}
	}
	one := []File{{Path: f.Path}}
	truncated, stopped, err := readUnified(strings.NewReader(patch), one, DefaultLimits)
	if err != nil || truncated || stopped || len(one[0].Hunks) != 1 {
		return errors.New("cannot apply the selected Arc hunk")
	}
	h := one[0].Hunks[0]
	var old, next strings.Builder
	for _, line := range h.Lines {
		text := line.raw
		if !line.NoNewline {
			text += "\n"
		}
		if line.Op != OpAdd {
			old.WriteString(text)
		}
		if line.Op != OpDelete {
			next.WriteString(text)
		}
	}
	skip := h.OldStart
	if h.OldLines > 0 {
		skip--
	}
	if skip < 0 {
		return errors.New("invalid Arc hunk start")
	}
	offset := 0
	for range skip {
		i := strings.IndexByte(before[offset:], '\n')
		if i < 0 {
			return errors.New("hunk no longer matches the Arc index; refresh the review")
		}
		offset += i + 1
	}
	if !strings.HasPrefix(before[offset:], old.String()) {
		return errors.New("hunk no longer matches the Arc index; refresh the review")
	}
	content := before[:offset] + next.String() + before[offset+old.Len():]
	// Refuse a concurrent change outside the hunk too. The daemon serializes
	// its own stage calls; an external Arc process may still change the index.
	if !f.indexLacks(unstage) {
		now, err := arc.Run(ctx, root, "show", ":"+f.Path)
		if err != nil {
			return err
		}
		if now != before {
			return errors.New("Arc index changed; refresh the review")
		}
	}
	cmd := arc.Command(ctx, root, "add", "-F", "-", "--", f.Path)
	cmd.Stdin = strings.NewReader(content)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("arc add -F: %s: %w", strings.TrimSpace(stderr.String()), err)
	}
	return nil
}

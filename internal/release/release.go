package release

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	Repo      = "afonsodelgado/factory"
	Checksums = "factory_checksums.txt"
)

type semver [3]int

func parse(s string) (semver, bool) {
	var v semver
	rest, hasPrefix := strings.CutPrefix(strings.TrimSpace(s), "v")
	parts := strings.Split(rest, ".")
	if !hasPrefix || len(parts) != 3 {
		return v, false
	}
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return v, false
		}
		v[i] = n
	}
	return v, true
}

func Valid(tag string) bool {
	_, ok := parse(tag)
	return ok
}

func Newer(latest, current string) bool {
	l, ok := parse(latest)
	if !ok {
		return false
	}
	c, ok := parse(current)
	if !ok {
		return true
	}
	for i := range l {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

func Asset() string {
	return fmt.Sprintf("factory_%s_%s", runtime.GOOS, runtime.GOARCH)
}

type GitHub struct {
	Base string
	HTTP *http.Client
}

func Default() GitHub {
	return GitHub{Base: "https://github.com/" + Repo, HTTP: &http.Client{}}
}

func (g GitHub) Latest(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.Base+"/releases/latest", nil)
	if err != nil {
		return "", err
	}
	client := *g.HTTP
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	tag := path.Base(resp.Header.Get("Location"))
	if resp.StatusCode/100 != 3 || !Valid(tag) {
		return "", fmt.Errorf("GitHub named no release (%s)", resp.Status)
	}
	return tag, nil
}

func (g GitHub) download(ctx context.Context, tag, asset string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.Base+"/releases/download/"+tag+"/"+asset, nil)
	if err != nil {
		return nil, err
	}
	resp, err := g.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s of %s: %s", asset, tag, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

func (g GitHub) Install(ctx context.Context, tag, target string) error {
	if !Valid(tag) {
		return fmt.Errorf("%q is not a release tag", tag)
	}
	sums, err := g.download(ctx, tag, Checksums)
	if err != nil {
		return fmt.Errorf("cannot download the checksums: %w", err)
	}
	want, ok := checksum(sums, Asset())
	if !ok {
		return fmt.Errorf("%s of %s lists no %s", Checksums, tag, Asset())
	}
	binary, err := g.download(ctx, tag, Asset())
	if err != nil {
		return fmt.Errorf("cannot download factory: %w", err)
	}
	got := sha256.Sum256(binary)
	if hex.EncodeToString(got[:]) != want {
		return fmt.Errorf("the downloaded %s of %s does not match its checksum, so nothing was installed", Asset(), tag)
	}
	return replace(target, binary)
}

func checksum(sums []byte, asset string) (string, bool) {
	scanner := bufio.NewScanner(bytes.NewReader(sums))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == asset {
			return strings.ToLower(fields[0]), true
		}
	}
	return "", false
}

func replace(target string, binary []byte) error {
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".factory-update-*")
	if err != nil {
		return fmt.Errorf("cannot write to %s, where factory is installed: %w", dir, err)
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(binary)
	err = errors.Join(err, tmp.Close(), os.Chmod(tmp.Name(), 0o755))
	if err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), target); err != nil {
		return fmt.Errorf("cannot replace %s: %w", target, err)
	}
	return nil
}

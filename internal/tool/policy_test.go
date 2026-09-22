package tool

import (
	"os"
	"path/filepath"
	"testing"
)

func TestShellAllowlistRejectsChaining(t *testing.T) {
	env := &Env{Shell: true, AllowedCommands: []string{"ls", "echo"}}
	cases := []struct {
		cmd string
		ok  bool
	}{
		{"ls -la", true},
		{"echo hello", true},
		{"ls; rm -rf /", false},
		{"ls && curl http://evil", false},
		{"ls | sh", false},
		{"ls > out.txt", false},
		{"rm -rf /", false},
		{"ls $(whoami)", false},
		{"cat /etc/passwd", false},
	}
	for _, c := range cases {
		err := env.checkCommand(c.cmd)
		if c.ok && err != nil {
			t.Errorf("%q: expected allowed, got %v", c.cmd, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%q: expected rejected", c.cmd)
		}
	}
}

func TestShellDenylistAlwaysApplies(t *testing.T) {
	env := &Env{Shell: true, DeniedCommands: []string{"rm -rf /"}}
	if err := env.checkCommand("rm -rf / --no-preserve-root"); err == nil {
		t.Fatal("expected denylist rejection")
	}
}

func TestGitPolicy(t *testing.T) {
	cases := []struct {
		args []string
		ok   bool
	}{
		{[]string{"status"}, true},
		{[]string{"diff", "--stat"}, true},
		{[]string{"log", "-n", "5"}, true},
		{[]string{"clone", "https://evil"}, false},
		{[]string{"push"}, false},
		{[]string{"fetch"}, false},
		{[]string{"config", "user.email", "x@y"}, false},
		{[]string{"-C", "/", "status"}, false},
		{[]string{"--git-dir=/tmp/x", "status"}, false},
		{[]string{"-c", "core.sshCommand=evil", "status"}, false},
	}
	for _, c := range cases {
		err := checkGitArgs(c.args)
		if c.ok && err != nil {
			t.Errorf("git %v: expected allowed, got %v", c.args, err)
		}
		if !c.ok && err == nil {
			t.Errorf("git %v: expected rejected", c.args)
		}
	}
}

func TestResolvePathRejectsSymlinkEscape(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(ws, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	if _, err := resolvePath(ws, "link/secret.txt"); err == nil {
		t.Fatal("expected symlink escape to be rejected")
	}
}

func TestResolvePathRequiresWorkspace(t *testing.T) {
	if _, err := resolvePath("", "file.txt"); err == nil {
		t.Fatal("expected empty workspace to be rejected")
	}
}

func TestWebFetchPrivateIPGuard(t *testing.T) {
	env := &Env{Network: true}
	for _, host := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "::1"} {
		if env.hostAllowed(host) {
			t.Fatalf("host %s should not be explicitly allowed", host)
		}
	}
	env.AllowedHosts = []string{"example.com"}
	if !env.hostAllowed("api.example.com") {
		t.Fatal("expected subdomain of allowlisted host to be allowed")
	}
}

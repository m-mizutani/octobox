package cli_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/m-mizutani/gt"

	"github.com/m-mizutani/octobox/pkg/cli"
	"github.com/m-mizutani/octobox/pkg/domain/interfaces"
	"github.com/m-mizutani/octobox/pkg/domain/model"
	"github.com/m-mizutani/octobox/pkg/usecase/usecasetest"
)

// isolate points HOME to a temporary directory and unsets every octobox
// variable: urfave/cli takes an empty variable as the flag value.
func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, name := range []string{"OCTOBOX_DB_PATH", "OCTOBOX_LOG_FILE", "OCTOBOX_LOG_LEVEL", "OCTOBOX_INTERVAL", "OCTOBOX_CLIENT_ID", "XDG_STATE_HOME"} {
		t.Setenv(name, "")
		gt.NoError(t, os.Unsetenv(name))
	}
	return home
}

type startCall struct {
	token    model.GitHubToken
	interval time.Duration
}

type fixture struct {
	stdout bytes.Buffer
	stderr bytes.Buffer
	store  *usecasetest.SecureStore
	device *usecasetest.DeviceAuthenticator
	client *usecasetest.GitHubClient
	starts []startCall
}

func newFixture() *fixture {
	return &fixture{
		store:  &usecasetest.SecureStore{},
		device: &usecasetest.DeviceAuthenticator{Token: "gho_new"},
		client: &usecasetest.GitHubClient{},
	}
}

func (f *fixture) run(ctx context.Context, args ...string) error {
	return cli.RunForTest(ctx, append([]string{"octobox"}, args...), cli.TestDeps{
		Stdout:      &f.stdout,
		Stderr:      &f.stderr,
		SecureStore: f.store,
		Device:      f.device,
		Client:      f.client,
		Browser:     &usecasetest.Browser{},
		StartInbox: func(_ context.Context, token model.GitHubToken, _ interfaces.Repository, interval time.Duration) error {
			f.starts = append(f.starts, startCall{token: token, interval: interval})
			return nil
		},
	})
}

func TestInboxWithoutToken(t *testing.T) {
	ctx := context.Background()
	isolate(t)

	t.Run("logs in first, then opens the inbox", func(t *testing.T) {
		f := newFixture()
		gt.NoError(t, f.run(ctx)).Required()
		gt.Equal(t, f.device.StartCalls, 1)
		gt.S(t, f.stdout.String()).Contains("First, copy your one-time code: ABCD-1234")
		gt.S(t, f.stdout.String()).Contains("Then open https://github.com/login/device in your browser and enter the code.")
		gt.A(t, f.starts).Equal([]startCall{{token: "gho_new", interval: time.Minute}})
		gt.Equal(t, f.store.Values[model.SecretKeyGitHubToken], "gho_new")
	})

	t.Run("denied authorization does not open the inbox", func(t *testing.T) {
		f := newFixture()
		f.device.WaitErr = interfaces.ErrAccessDenied
		err := f.run(ctx)
		gt.Error(t, err).Is(interfaces.ErrAccessDenied)
		gt.A(t, f.starts).Length(0)
		gt.S(t, f.stderr.String()).Contains("authorization was denied")
	})

	t.Run("stored token skips the device flow", func(t *testing.T) {
		f := newFixture()
		f.store.Values = map[model.SecretKey]string{model.SecretKeyGitHubToken: "gho_stored"}
		gt.NoError(t, f.run(ctx)).Required()
		gt.Equal(t, f.device.StartCalls, 0)
		gt.A(t, f.starts).Equal([]startCall{{token: "gho_stored", interval: time.Minute}})
	})
}

func TestDefaultPaths(t *testing.T) {
	ctx := context.Background()
	home := isolate(t)

	f := newFixture()
	f.store.Values = map[model.SecretKey]string{model.SecretKeyGitHubToken: "gho_stored"}
	gt.NoError(t, f.run(ctx)).Required()

	dir := filepath.Join(home, ".octobox")
	info, err := os.Stat(dir)
	gt.NoError(t, err).Required()
	gt.Equal(t, info.Mode().Perm(), os.FileMode(0o700))
	gt.F(t, filepath.Join(dir, "octobox.db")).Exists()
	gt.F(t, filepath.Join(dir, "octobox.log")).Exists()

	t.Run("XDG_STATE_HOME does not change the location", func(t *testing.T) {
		home := isolate(t)
		t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
		f := newFixture()
		f.store.Values = map[model.SecretKey]string{model.SecretKeyGitHubToken: "gho_stored"}
		gt.NoError(t, f.run(ctx)).Required()
		gt.F(t, filepath.Join(home, ".octobox", "octobox.db")).Exists()
		gt.F(t, filepath.Join(home, "state")).NotExists()
	})
}

func TestIntervalFlag(t *testing.T) {
	ctx := context.Background()
	isolate(t)

	f := newFixture()
	f.store.Values = map[model.SecretKey]string{model.SecretKeyGitHubToken: "gho_stored"}
	err := f.run(ctx, "--interval", "20s")
	gt.Error(t, err)
	gt.S(t, f.stderr.String()).Contains("--interval must be at least 30s")
	gt.A(t, f.starts).Length(0)

	t.Setenv("OCTOBOX_INTERVAL", "90s")
	f = newFixture()
	f.store.Values = map[model.SecretKey]string{model.SecretKeyGitHubToken: "gho_stored"}
	gt.NoError(t, f.run(ctx)).Required()
	gt.A(t, f.starts).Equal([]startCall{{token: "gho_stored", interval: 90 * time.Second}})
}

func TestAuthCommands(t *testing.T) {
	ctx := context.Background()
	isolate(t)

	f := newFixture()
	gt.NoError(t, f.run(ctx, "auth", "login")).Required()
	gt.S(t, f.stdout.String()).Contains("First, copy your one-time code: ABCD-1234\n" +
		"Then open https://github.com/login/device in your browser and enter the code.\n" +
		"Waiting for authorization...\n" +
		"Logged in as alice\n")
	gt.Equal(t, f.store.Values[model.SecretKeyGitHubToken], "gho_new")

	gt.NoError(t, f.run(ctx, "auth", "logout")).Required()
	_, ok := f.store.Values[model.SecretKeyGitHubToken]
	gt.False(t, ok)

	t.Run("login error messages", func(t *testing.T) {
		cases := map[error]string{
			interfaces.ErrDeviceCodeExpired:  "the code expired; run `octobox auth login` again",
			interfaces.ErrDeviceFlowDisabled: "device flow is disabled for the OAuth app; enable it in the app settings",
		}
		for wantErr, msg := range cases {
			f := newFixture()
			f.device.WaitErr = wantErr
			gt.Error(t, f.run(ctx, "auth", "login")).Is(wantErr)
			gt.Equal(t, f.stderr.String(), "octobox: "+msg+"\n")
		}

		f := newFixture()
		f.store.SetErr = os.ErrPermission
		gt.Error(t, f.run(ctx, "auth", "login"))
		gt.Equal(t, f.stderr.String(), "octobox: failed to store the token in the OS secure storage\n")

		f = newFixture()
		f.store.GetErr = interfaces.ErrSecureStoreUnavailable
		gt.Error(t, f.run(ctx))
		gt.Equal(t, f.stderr.String(), "octobox: secure storage is not available on this OS\n")
	})
}

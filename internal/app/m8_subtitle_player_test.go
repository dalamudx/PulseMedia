package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestM8SubtitleNativePlayerPG(t *testing.T) {
	if os.Getenv("PULSE_M8_SUBTITLE_PLAYER_ACCEPTANCE") != "1" {
		t.Skip("owned mpv player and playable media required")
	}
	video, err := os.ReadFile(os.Getenv("PULSE_M8_SUBTITLE_PLAYER_VIDEO"))
	if err != nil {
		t.Fatal(err)
	}
	m8SubtitleRuntime(t, "movie", "publish", m8SubtitlePlaybackCheck{video: video, verify: m8VerifyMPVSubtitle})
}

func m8VerifyMPVSubtitle(t *testing.T, root, relative, subtitle string) {
	t.Helper()
	image := os.Getenv("PULSE_M8_SUBTITLE_PLAYER_IMAGE")
	if !strings.HasPrefix(image, "localhost/pulse-m8-player:") {
		t.Fatal("owned player image required")
	}
	ipc, err := os.MkdirTemp(os.Getenv("TMPDIR"), "mpv-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(ipc); err != nil {
			t.Error(err)
		}
	})
	socket := filepath.Join(ipc, "player.sock")
	if len(socket) > 100 {
		t.Fatal("owned IPC path too long")
	}
	name := "pulse-m8-player-" + uuid.NewString()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	cmd := exec.CommandContext(ctx, "podman", "run", "--rm", "--pull=never", "--name", name, "--network=none", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--read-only", "--mount", "type=bind,source="+root+",destination=/media,readonly", "--mount", "type=bind,source="+ipc+",destination=/data/temp", image, "--no-config", "--idle=yes", "--pause=yes", "--vo=null", "--ao=null", "--audio=no", "--sub-auto=fuzzy", "--input-ipc-server=/data/temp/player.sock")
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err = cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer stopCancel()
		_ = exec.CommandContext(stopCtx, "podman", "rm", "--force", "--time", "5", name).Run()
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("player process did not return")
		}
		err := exec.CommandContext(stopCtx, "podman", "container", "exists", name).Run()
		var absent *exec.ExitError
		if !errors.As(err, &absent) || absent.ExitCode() != 1 {
			t.Error("owned player container cleanup not verified", err)
		}
	})
	var conn net.Conn
	for conn == nil {
		conn, err = net.DialTimeout("unix", socket, 100*time.Millisecond)
		if err == nil {
			break
		}
		select {
		case <-done:
			t.Fatal("native player exited before IPC was ready")
		case <-ctx.Done():
			t.Fatal("native player IPC deadline")
		case <-time.After(20 * time.Millisecond):
		}
	}
	defer func() { _ = conn.Close() }()
	deadline, _ := ctx.Deadline()
	if err = conn.SetDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(io.LimitReader(conn, 128*1024))
	encoder := json.NewEncoder(conn)
	type message struct {
		Event string          `json:"event"`
		Error string          `json:"error"`
		ID    int             `json:"request_id"`
		Data  json.RawMessage `json:"data"`
	}
	send := func(id int, args ...any) {
		t.Helper()
		if err := encoder.Encode(map[string]any{"command": args, "request_id": id}); err != nil {
			t.Fatal(err)
		}
	}
	receive := func() message {
		t.Helper()
		var m message
		if err := decoder.Decode(&m); err != nil {
			t.Fatal("native player IPC", err)
		}
		if m.Error != "" && m.Error != "success" {
			t.Fatal("native player command rejected", m.Error)
		}
		return m
	}
	send(1, "loadfile", filepath.ToSlash(filepath.Join("/media", relative)), "replace")
	for receive().Event != "file-loaded" {
	}
	send(2, "get_property", "track-list")
	var tracks []struct {
		Type     string `json:"type"`
		Codec    string `json:"codec"`
		External bool   `json:"external"`
		Selected bool   `json:"selected"`
		Filename string `json:"external-filename"`
	}
	for {
		m := receive()
		if m.ID == 2 {
			if err = json.Unmarshal(m.Data, &tracks); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	expected := filepath.ToSlash(filepath.Join("/media", filepath.Dir(relative), filepath.Base(subtitle)))
	videos, subtitles := 0, 0
	for _, track := range tracks {
		if track.Type == "video" {
			videos++
		}
		if track.Type == "sub" {
			subtitles++
			if !track.External || !track.Selected || track.Filename != expected || (track.Codec != "subrip" && track.Codec != "srt") {
				t.Fatalf("native auto-discovered track mismatch: %+v", track)
			}
		}
	}
	if videos != 1 || subtitles != 1 {
		t.Fatalf("native track counts: video=%d subtitles=%d", videos, subtitles)
	}
	send(3, "seek", 1.5, "absolute+exact")
	seeking := false
	for {
		m := receive()
		if m.Event == "seek" {
			seeking = true
		}
		if seeking && m.Event == "playback-restart" {
			break
		}
	}
	send(4, "get_property", "sub-text")
	for {
		m := receive()
		if m.ID == 4 {
			var text string
			if err = json.Unmarshal(m.Data, &text); err != nil || !strings.Contains(text, "你好，世界。") {
				t.Fatal("native player did not decode the Chinese cue", err)
			}
			break
		}
	}
	t.Log("M8_SUBTITLE_PLAYER_PASS player=mpv auto_profile=fuzzy REAL_PLAYABLE_VIDEO=true source=APP_WORKER automatic_external=true selected=true codec=subrip Chinese_cue_decoded=true explicit_sub_file=false network=OWNED_HTTP live_shooter=false")
}

package app

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/dmillzilla/hane/internal/mailimage"
)

func imageModel(t *testing.T) model {
	t.Helper()
	m := newModel(time.Now())
	m.openThread()
	return m
}

func runImageKey(m model) (model, tea.Cmd) {
	next, cmd := m.Update(tea.KeyPressMsg{Code: 'I', Text: "I"})
	return next.(model), cmd
}

func remoteImageModel(t *testing.T) model {
	m := imageModel(t)
	target := m.current()
	for id, meta := range target.ImageBodies {
		for i := range meta.Images {
			meta.Images[i].Source = "https://images.example/photo.png"
		}
		target.ImageBodies[id] = meta
	}
	return m
}

func TestRemoteImagesAskOnceAndStayInline(t *testing.T) {
	m := remoteImageModel(t)
	calls := 0
	m.images.testLoader = func(context.Context, imageJob) (image.Image, int64, error) {
		calls++
		return demoPostcard(), 100, nil
	}
	next, cmd := m.Update(tea.WindowSizeMsg{Width: 120, Height: 36})
	m = next.(model)
	before := ansi.Strip(m.View().Content)
	if cmd != nil || calls != 0 || strings.Count(before, "Always show") != 1 || strings.Contains(before, "▀") {
		t.Fatal("opening mail fetched remote images without consent or repeated the notice")
	}
	m, cmd = runImageKey(m)
	if cmd == nil || !m.preferences.RemoteImages || !strings.Contains(m.notice, "Loading") || calls != 0 {
		t.Fatal("load action did not schedule the preview separately from rendering")
	}
	next, cmd = m.Update(cmd())
	m = next.(model)
	loaded := strings.Join(m.readerLines(), "\n")
	if cmd != nil || calls != 1 || !strings.Contains(loaded, "▀") || strings.Contains(loaded, "[[hane-image:") || strings.Contains(m.View().Content, "Always show images") {
		t.Fatal("loaded image did not render inline")
	}
	m = press(m, "Sh") // turn the saved preference off in Settings
	if strings.Contains(strings.Join(m.readerLines(), "\n"), "▀") {
		t.Fatal("turning off remote images kept the cached preview visible")
	}
	m = special(m, tea.KeyEscape)
	m, cmd = runImageKey(m)
	if cmd != nil || calls != 1 || !strings.Contains(strings.Join(m.readerLines(), "\n"), "▀") {
		t.Fatal("showing a cached image fetched it again")
	}
}

func TestImageLoadingCancellationAndStaleResults(t *testing.T) {
	m := remoteImageModel(t)
	m.images.testLoader = func(context.Context, imageJob) (image.Image, int64, error) { return demoPostcard(), 100, nil }
	m, cmd := runImageKey(m)
	result := cmd()
	m = press(m, "Sh") // disable remote images before the completion is delivered
	next, nextCmd := m.Update(result)
	m = next.(model)
	if nextCmd != nil || len(m.images.loaded) != 0 || strings.Contains(strings.Join(m.readerLines(), "\n"), "▀") {
		t.Fatal("a stale image completion reopened a hidden preview")
	}
	m = special(m, tea.KeyEscape)
	m, cmd = runImageKey(m)
	result = cmd()
	m = press(m, "h")
	next, _ = m.Update(result)
	m = next.(model)
	if m.reading || len(m.images.loaded) != 0 {
		t.Fatal("leaving the conversation accepted a stale image")
	}
}

func TestImageFailureIsInlineAndDoesNotReplaceMessageText(t *testing.T) {
	m := imageModel(t)
	m.images.testLoader = func(context.Context, imageJob) (image.Image, int64, error) {
		return nil, 0, errors.New("Image host unavailable")
	}
	before := m.current().newest().Body
	m, cmd := runImageKey(m)
	next, again := m.Update(cmd())
	m = next.(model)
	if again != nil || m.current().newest().Body != before || !strings.Contains(ansi.Strip(strings.Join(m.readerLines(), "\n")), "Image host unavailable") {
		t.Fatal("image failure damaged the body or was not shown inline")
	}
	next, again = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if again != nil {
		t.Fatal("a failed image retried on a redraw")
	}
}

func TestLoadedImageViewsFitResizedTerminals(t *testing.T) {
	m := imageModel(t)
	m, cmd := runImageKey(m)
	next, _ := m.Update(cmd())
	m = next.(model)
	for _, size := range [][2]int{{160, 44}, {120, 36}, {90, 24}, {48, 16}} {
		next, _ := m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m = next.(model)
		lines := strings.Split(m.View().Content, "\n")
		if len(lines) != size[1] {
			t.Fatal("image view exceeded terminal height")
		}
		for _, line := range lines {
			if ansi.StringWidth(line) != size[0] {
				t.Fatal("image view exceeded terminal width")
			}
		}
	}
}

func TestMessageImageIdentitySurvivesReordering(t *testing.T) {
	m := imageModel(t)
	target := m.current()
	img := target.ImageBodies[target.Messages[0].ID]
	if len(img.Images) == 0 {
		t.Fatal("missing demo image")
	}
	target.Messages = append([]Message{{Sender: "New reply", Address: "friend@example.com", Body: "A newer reply without an image", Received: time.Now().Add(time.Hour), ID: "new-id"}}, target.Messages...)
	first := m.imageBodyLines(target, 0, target.Messages[0], 60)
	second := m.imageBodyLines(target, 1, target.Messages[1], 60)
	if strings.Contains(strings.Join(first, "\n"), "Image:") || !strings.Contains(strings.Join(second, "\n"), "Image:") {
		t.Fatal("an image was associated with a different message after reordering")
	}
}

func TestOfflineModeDoesNotScheduleRemoteOrAttachedImages(t *testing.T) {
	m := liveTestModel(t)
	m.live.offline = true
	key := m.liveKey(0)
	a := m.live.disk.Accounts[key]
	remote := messageImage{ID: "remote", Name: "Remote", Source: "https://images.example/photo.png"}
	attachment := messageImage{ID: "attached", Name: "Attached", Part: []int{2}, MIMEType: "image/png"}
	a.Messages[0].Images = []messageImage{remote, attachment}
	a.Messages[0].ImageBody = remote.token() + "\n" + attachment.token()
	m.live.disk.Accounts[key] = a
	m.rebuildLive()
	m.openThread()
	calls := 0
	m.images.testLoader = func(context.Context, imageJob) (image.Image, int64, error) {
		calls++
		return nil, 0, errors.New("must not load")
	}
	m, cmd := runImageKey(m)
	if cmd != nil || calls != 0 || !strings.Contains(ansi.Strip(strings.Join(m.readerLines(), "\n")), "Offline") {
		t.Fatal("offline image loading scheduled a remote or IMAP fetch")
	}
}

func TestEmbeddedImagesCanLoadOffline(t *testing.T) {
	m := liveTestModel(t)
	m.live.offline = true
	key := m.liveKey(0)
	a := m.live.disk.Accounts[key]
	data := imagePNG(t)
	img := messageImage{ID: "embedded", Name: "Embedded", Source: "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)}
	a.Messages[0].Images, a.Messages[0].ImageBody = []messageImage{img}, img.token()
	m.live.disk.Accounts[key] = a
	m.rebuildLive()
	m.openThread()
	next, cmd := m.Update(tea.WindowSizeMsg{Width: 120, Height: 36})
	m = next.(model)
	if cmd == nil {
		t.Fatal("offline embedded image did not schedule local decoding")
	}
	next, _ = m.Update(cmd())
	m = next.(model)
	if !strings.Contains(strings.Join(m.readerLines(), "\n"), "▀") {
		t.Fatal("embedded image was not displayed offline")
	}
}

func TestEmbeddedImageLoadsOnOpeningWithoutEnablingRemoteImages(t *testing.T) {
	m := newModel(time.Now())
	next, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(model)
	if cmd == nil || m.preferences.RemoteImages {
		t.Fatal("opening embedded image did not schedule it independently of the remote preference")
	}
	next, cmd = m.Update(cmd())
	m = next.(model)
	if cmd != nil || !strings.Contains(strings.Join(m.readerLines(), "\n"), "▀") || m.preferences.RemoteImages {
		t.Fatal("embedded preview was not loaded automatically")
	}
}

func TestDemoRemoteImageCanBeClickedAndHiddenWithoutNetwork(t *testing.T) {
	m := newModel(time.Now())
	m.demo = true
	found := false
	for i, thread := range m.folderThreads() {
		if thread.Subject == "A few photos from the coast" {
			m.selected, found = i, true
			break
		}
	}
	if !found {
		t.Fatal("missing image-button demo conversation")
	}
	next, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(model)
	if cmd != nil || !m.blockedRemoteImages() || strings.Contains(strings.Join(m.readerLines(), "\n"), "▀") {
		t.Fatal("demo remote sample did not start behind the consent action")
	}
	x, y := imageButtonPosition(t, m)
	next, cmd = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x + 4, Y: y})
	m = next.(model)
	if cmd == nil || !m.preferences.RemoteImages || m.preferencesPath != "" {
		t.Fatal("demo click did not schedule a session-only preview")
	}
	// Use the real loader, with no credentials, backend, or injected image data.
	next, again := m.Update(cmd())
	m = next.(model)
	if again != nil || !strings.Contains(strings.Join(m.readerLines(), "\n"), "▀") || m.blockedRemoteImages() {
		t.Fatal("demo click failed to display its local picture")
	}
	m = special(press(m, "Sh"), tea.KeyEscape)
	if !m.blockedRemoteImages() || strings.Contains(strings.Join(m.readerLines(), "\n"), "▀") {
		t.Fatal("turning demo images off did not restore the button")
	}
	imageButtonPosition(t, m)
}

func TestDemoLoaderRejectsNetworkAndMailboxRequests(t *testing.T) {
	load := newImageLoader(nil, nil)
	for _, img := range []messageImage{
		{Source: "https://images.example/not-a-demo-image.png"},
		{Part: []int{2}, MIMEType: "image/png"},
	} {
		raster, _, err := load(context.Background(), imageJob{Demo: true, Image: img, Limit: mailimage.MaxBytes})
		if raster != nil || err == nil || err.Error() != "this image is not part of the offline demo" {
			t.Fatal("demo job reached the HTTP or IMAP loading path")
		}
	}
}

func TestRememberedChoiceLoadsOtherConversations(t *testing.T) {
	m := remoteImageModel(t)
	first := *m.current()
	second := first
	second.ID++
	second.Subject = "A different conversation"
	second.Messages = append([]Message(nil), first.Messages...)
	second.Messages[0].Received = first.newest().Received.Add(-time.Second)
	m.threads = []Thread{first, second}
	calls := 0
	m.images.testLoader = func(context.Context, imageJob) (image.Image, int64, error) {
		calls++
		return demoPostcard(), 100, nil
	}
	m, cmd := runImageKey(m)
	next, _ := m.Update(cmd())
	m = press(next.(model), "hj")
	next, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(model)
	if cmd == nil || m.current().ID != second.ID || !m.preferences.RemoteImages || m.blockedRemoteImages() {
		t.Fatal("saved choice was not applied to the next conversation")
	}
	next, _ = m.Update(cmd())
	if calls != 2 || !strings.Contains(strings.Join(next.(model).readerLines(), "\n"), "▀") {
		t.Fatal("the next conversation required another load action")
	}
}

func imageButtonPosition(t *testing.T, m model) (int, int) {
	t.Helper()
	for y, line := range strings.Split(ansi.Strip(m.View().Content), "\n") {
		if pos := strings.Index(line, "[ Always show images ]"); pos >= 0 {
			return ansi.StringWidth(line[:pos]), y
		}
	}
	t.Fatal("no visible image button")
	return 0, 0
}

func TestClickingImageButtonLoadsAndRemembersAcrossLayouts(t *testing.T) {
	for _, size := range [][2]int{{160, 44}, {125, 36}, {120, 36}, {90, 24}, {48, 16}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			m := remoteImageModel(t)
			m.width, m.height = size[0], size[1]
			m.preferencesPath = filepath.Join(t.TempDir(), "preferences.json")
			calls := 0
			m.images.testLoader = func(context.Context, imageJob) (image.Image, int64, error) {
				calls++
				return demoPostcard(), 1, nil
			}
			x, y := imageButtonPosition(t, m)
			m = press(m, "G")
			if m.scroll == 0 {
				t.Fatal("fixture must have scrolling content")
			}
			if sx, sy := imageButtonPosition(t, m); sx != x || sy != y {
				t.Fatal("image action scrolled away from the visible message")
			}
			next, cmd := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x + 5, Y: y})
			m = next.(model)
			if cmd == nil || calls != 0 || !m.preferences.RemoteImages || m.focus != threadPane {
				t.Fatal("click did not schedule the image and enable the preference")
			}
			p, err := readPreferences(m.preferencesPath)
			if err != nil || !p.RemoteImages {
				t.Fatal("mouse action did not save the preference")
			}
			next, again := m.Update(cmd())
			m = next.(model)
			if again != nil || calls != 1 || strings.Contains(m.View().Content, "Always show images") || !strings.Contains(strings.Join(m.readerLines(), "\n"), "▀") {
				t.Fatal("click did not load the preview and dismiss the notice")
			}
		})
	}
}

func TestClicksOutsideImageButtonDoNotEnableRemoteImages(t *testing.T) {
	for _, click := range []struct {
		name   string
		dx, dy int
		button tea.MouseButton
	}{
		{"left margin", -1, 0, tea.MouseLeft},
		{"explanation", 0, -1, tea.MouseLeft},
		{"preference caption", 0, 1, tea.MouseLeft},
		{"right padding", 22, 0, tea.MouseLeft},
		{"right mouse button", 0, 0, tea.MouseRight},
	} {
		t.Run(click.name, func(t *testing.T) {
			m := remoteImageModel(t)
			x, y := imageButtonPosition(t, m)
			next, cmd := m.Update(tea.MouseClickMsg{Button: click.button, X: x + click.dx, Y: y + click.dy})
			if cmd != nil || next.(model).preferences.RemoteImages {
				t.Fatal("click outside the button enabled remote images")
			}
		})
	}
}

func TestImageMetadataArrivingFromSyncLoadsWithoutAnotherAction(t *testing.T) {
	for _, remote := range []bool{false, true} {
		t.Run(fmt.Sprintf("remote=%v", remote), func(t *testing.T) {
			m := liveTestModel(t)
			m.openThread()
			m.live.pending = nil // the thread lookup is represented by the result below
			m.live.queued = map[string]syncRequest{}
			m.preferences.RemoteImages = remote
			key := m.liveKey(0)
			data := copyAccount(m.live.disk.Accounts[key])
			var applied []string
			for _, op := range data.Flags {
				applied = append(applied, op.ID)
			}
			img := messageImage{ID: "sync-image", Name: "Photo", Part: []int{2}, MIMEType: "image/png"}
			if remote {
				img.Source, img.Part = "https://images.example/photo.png", nil
			}
			data.Messages[0].Images, data.Messages[0].ImageBody = []messageImage{img}, img.token()
			data.Messages[0].ImagesIndexed = true
			calls := 0
			m.images.testLoader = func(_ context.Context, job imageJob) (image.Image, int64, error) {
				calls++
				if !remote && (len(job.Image.Part) != 1 || job.Password != "test-password" || job.Ref.UID != 41) {
					t.Fatal("attached preview lost its mail identity or connection details")
				}
				return demoPostcard(), 100, nil
			}
			next, cmd := m.Update(liveSyncMsg{Key: key, Request: syncRequest{Folder: "INBOX"}, Result: syncResult{Data: data, Applied: applied}})
			m = next.(model)
			if cmd == nil {
				t.Fatal("sync delivered image metadata but no preview was scheduled")
			}
			next, again := m.Update(cmd())
			m = next.(model)
			if again != nil || calls != 1 || !strings.Contains(strings.Join(m.readerLines(), "\n"), "▀") {
				t.Fatal("image required a manual action after the message synced")
			}
		})
	}
}

func TestConversationImageCommandsDoNotChangeGlobalPreference(t *testing.T) {
	m := remoteImageModel(t)
	m.images.testLoader = func(context.Context, imageJob) (image.Image, int64, error) { return demoPostcard(), 1, nil }
	m = press(m, ":loadimages")
	next, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(model)
	if cmd == nil || m.preferences.RemoteImages {
		t.Fatal("one-conversation load changed the saved preference")
	}
	next, _ = m.Update(cmd())
	m = press(next.(model), ":hideimages")
	m = special(m, tea.KeyEnter)
	if m.preferences.RemoteImages || strings.Contains(strings.Join(m.readerLines(), "\n"), "▀") || m.images.thread != "" {
		t.Fatal("one-conversation hide did not hide and cancel previews")
	}
}

func TestImageQueueHasDownloadAndConcurrencyBounds(t *testing.T) {
	m := imageModel(t)
	target := m.current()
	id := target.Messages[0].ID
	var meta messageImages
	for i := 0; i < 40; i++ {
		img := messageImage{ID: fmt.Sprint(i), Name: "Image", Source: fmt.Sprintf("https://images.example/%d.png", i)}
		meta.Images = append(meta.Images, img)
		meta.Text += img.token() + "\n"
	}
	target.ImageBodies[id] = meta
	calls := 0
	m.images.testLoader = func(context.Context, imageJob) (image.Image, int64, error) {
		calls++
		return demoPostcard(), mailimage.MaxBytes, nil
	}
	m, cmd := runImageKey(m)
	if len(m.images.busy) != maxThreadImages {
		t.Fatal("image work was not bounded by the per-action limit")
	}
	for cmd != nil {
		next, nextCmd := m.Update(cmd())
		m, cmd = next.(model), nextCmd
	}
	if calls != imageDownloadBudget/mailimage.MaxBytes || len(m.images.busy) != 0 || !strings.Contains(m.notice, "limit") {
		t.Fatalf("image queue exceeded the download budget or kept running: calls=%d notice=%s", calls, m.notice)
	}
}

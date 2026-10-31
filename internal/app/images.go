package app

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/dmillzilla/hane/internal/mailimage"
)

const maxThreadImages = 32
const imageDownloadBudget = 32 << 20

// This sample follows the remote-image consent policy, but the demo loader
// supplies its picture locally. Demo mode never contacts this address.
const demoRemoteImageSource = "https://images.example/hane-demo-postcard.png"

type imageJob struct {
	Key      string
	Image    messageImage
	Ref      mailRef
	Config   AccountConfig
	Password string
	Demo     bool
	Limit    int64
}

type imageLoader func(context.Context, imageJob) (image.Image, int64, error)
type imagePreview struct {
	raster image.Image
	lines  map[string][]string
}
type imageState struct {
	visible    map[string]bool
	hidden     map[string]bool
	loaded     map[string]*imagePreview
	failed     map[string]string
	busy       map[string]bool
	order      []string
	queue      []imageJob
	generation uint64
	thread     string
	lastThread string
	remaining  int64
	ctx        context.Context
	cancel     context.CancelFunc
	load       imageLoader
	// Tests supply a local loader; production constructs a fresh loader for
	// each request, so its private credential cache belongs to one worker.
	testLoader imageLoader
}
type imageLoadedMsg struct {
	key        string
	generation uint64
	raster     image.Image
	used       int64
	err        error
}

func newImageState() *imageState {
	return &imageState{visible: map[string]bool{}, hidden: map[string]bool{}, loaded: map[string]*imagePreview{}, failed: map[string]string{}, busy: map[string]bool{}}
}

type imagePartFetcher interface {
	Image(context.Context, AccountConfig, string, mailRef, messageImage, int64) ([]byte, string, error)
}

func newImageLoader(store credentialStore, backend mailTransport) imageLoader {
	passwords := map[string]string{}
	return func(ctx context.Context, job imageJob) (image.Image, int64, error) {
		var data []byte
		var kind string
		var err error
		switch {
		case job.Demo && (job.Image.Source == "demo:postcard" || job.Image.Source == demoRemoteImageSource):
			return demoPostcard(), 0, nil
		case strings.HasPrefix(strings.ToLower(job.Image.Source), "data:"):
			data, kind, err = mailimage.Data(job.Image.Source)
		case job.Demo:
			return nil, 0, errors.New("this image is not part of the offline demo")
		case job.Image.Source != "":
			data, kind, err = mailimage.Fetch(ctx, job.Image.Source, job.Limit)
		case len(job.Image.Part) > 0:
			fetcher, ok := backend.(imagePartFetcher)
			if !ok || store == nil {
				return nil, 0, errors.New("image attachment loading is unavailable")
			}
			key := credentialKey(job.Config)
			password := job.Password
			if password == "" {
				password = passwords[key]
			}
			if password == "" {
				password, err = store.Get(ctx, job.Config)
				if err != nil {
					return nil, 0, errors.New("unlock the local keyring to load attached images")
				}
				passwords[key] = password
			}
			data, kind, err = fetcher.Image(ctx, job.Config, password, job.Ref, job.Image, job.Limit)
		default:
			err = errors.New("image source is unavailable")
		}
		if err != nil {
			return nil, job.Limit, err
		}
		if err := ctx.Err(); err != nil {
			return nil, int64(len(data)), err
		}
		if int64(len(data)) > job.Limit {
			return nil, int64(len(data)), errors.New("image exceeds the download limit")
		}
		raster, err := mailimage.Decode(data, kind)
		return raster, int64(len(data)), err
	}
}

func (m model) imageThreadKey(t *Thread) string {
	if t == nil {
		return ""
	}
	return fmt.Sprintf("%s/%d/%d", m.liveKey(t.Account), t.Account, t.ID)
}

func (m model) imageKey(t *Thread, id string, meta messageImages, img messageImage) string {
	return imageID(fmt.Sprintf("%s/%s/%s/%s", m.imageThreadKey(t), id, meta.Ref.key(), img.ID))
}

func (m *model) cancelImages() {
	if m.images == nil {
		return
	}
	if m.images.cancel != nil {
		m.images.cancel()
	}
	m.images.generation++
	m.images.cancel, m.images.queue, m.images.thread = nil, nil, ""
	m.images.busy = map[string]bool{}
}

func (m *model) cancelImagesIfNeeded() {
	if m.images != nil && m.images.thread != "" && (!m.reading || m.imageThreadKey(m.current()) != m.images.thread) {
		m.cancelImages()
	}
}

func (m *model) hideImages() {
	if m.images == nil {
		return
	}
	m.images.visible[m.imageThreadKey(m.current())] = false
	m.images.hidden[m.imageThreadKey(m.current())] = true
	m.cancelImages()
	m.notice = "Images hidden for this conversation. I shows them again; S opens settings."
	m.clampScroll()
}

func remoteImage(img messageImage) bool {
	source := strings.ToLower(img.Source)
	return source != "" && !strings.HasPrefix(source, "data:") && !strings.HasPrefix(source, "demo:")
}

func (m model) imageVisible(t *Thread, img messageImage) bool {
	if m.images != nil && m.images.hidden[m.imageThreadKey(t)] {
		return false
	}
	return !remoteImage(img) || m.preferences.RemoteImages || m.images != nil && m.images.visible[m.imageThreadKey(t)]
}

func (m model) blockedRemoteImages() bool {
	return m.blockedRemoteImageCount() > 0
}

func (m model) blockedRemoteImageCount() int {
	t := m.current()
	if t == nil || t.QueueID != "" || isDraftThread(t) {
		return 0
	}
	count := 0
	for _, meta := range t.ImageBodies {
		for _, img := range meta.Images {
			if remoteImage(img) && !m.imageVisible(t, img) {
				count++
			}
		}
	}
	return count
}

type readerImageNotice struct {
	lines       []string
	buttonRow   int
	buttonWidth int
}

// The rendered button and its mouse target share one layout. The notice sits
// above the scrolling body, so the action stays available beside hidden images.
func (m model) imageNotice() readerImageNotice {
	count := m.blockedRemoteImageCount()
	if !m.reading || count == 0 {
		return readerImageNotice{}
	}
	noun := "images"
	if count == 1 {
		noun = "image"
	}
	notice := readerImageNotice{}
	compact := m.bodyHeight() < 16
	if !compact {
		text := fmt.Sprintf("%d remote %s hidden. Loading can tell the sender you opened this mail.", count, noun)
		for _, line := range strings.Split(ansi.Wrap(text, m.readerWidth(), ""), "\n") {
			notice.lines = append(notice.lines, styled(line, muted))
		}
	}
	const button = "[ Always show images ]"
	notice.buttonRow, notice.buttonWidth = len(notice.lines), ansi.StringWidth(button)
	notice.lines = append(notice.lines, imageNoticeButton(button)+styled("  click or press I", muted))
	if compact {
		caption := "All mail · senders may see opens · S off"
		if m.demo {
			caption = "Demo · no network · S to turn off"
		}
		notice.lines = append(notice.lines, styled(caption, muted))
	} else {
		caption := "All mail · remembered · S to turn off"
		if m.demo {
			caption = "Demo · no network · session only · S off"
		}
		notice.lines = append(notice.lines, styled(caption, muted), "")
	}
	return notice
}

func (m model) imageNoticeLines() []string {
	return m.imageNotice().lines
}

func (m model) imageNoticeClicked(x, y int) bool {
	tw, lw, rw := m.layout()
	if m.width < 48 || m.height < 16 || rw == 0 || m.loading || m.help || m.compose != nil || m.settingsOpen || m.setup != nil || m.quitOpen || m.deletion != nil {
		return false
	}
	notice := m.imageNotice()
	left := tw + lw + 2 // pane margin, then the reader's content indent
	return notice.buttonWidth > 0 && x >= left && x < left+notice.buttonWidth && y == 7+notice.buttonRow
}

// This is a persistent app-wide choice, made by the reader notice or I. Merely
// opening mail never changes the remote image preference.
func (m *model) alwaysShowImages() tea.Cmd {
	if err := m.setRemoteImages(true); err != nil {
		m.notice = err.Error()
		return nil
	}
	if m.images == nil {
		m.images = newImageState()
	}
	delete(m.images.hidden, m.imageThreadKey(m.current()))
	if !m.reading {
		m.openThread()
	}
	m.cancelImages()
	m.notice = "Remote images will always show. S opens reading settings."
	if m.demo {
		m.notice = "Demo images will show for this session. S opens reading settings."
	}
	return m.queueImages(true)
}

func (m *model) loadImages() tea.Cmd {
	t := m.current()
	if t == nil || t.QueueID != "" || isDraftThread(t) {
		m.notice = "Open a conversation to load images."
		return nil
	}
	if !m.reading {
		m.openThread()
	}
	if m.images == nil {
		m.images = newImageState()
	}
	m.cancelImages()
	state := m.images
	thread := m.imageThreadKey(t)
	state.visible[thread] = true
	delete(state.hidden, thread)
	return m.queueImages(true)
}

func (m *model) automaticImages() tea.Cmd {
	if !m.reading || m.loading || m.compose != nil || m.help || m.settingsOpen || m.setup != nil || m.quitOpen || m.deletion != nil {
		return nil
	}
	return m.queueImages(false)
}

// Local images load as a conversation opens. Remote images join the same queue
// only after an explicit saved preference (or the one-conversation command).
func (m *model) queueImages(retry bool) tea.Cmd {
	t := m.current()
	if t == nil || t.QueueID != "" || isDraftThread(t) {
		return nil
	}
	if m.images == nil {
		m.images = newImageState()
	}
	state := m.images
	if state.thread != "" {
		return nil
	}
	thread := m.imageThreadKey(t)
	if state.lastThread != thread {
		if len(state.failed) >= 128 {
			state.failed = map[string]string{}
		}
		state.lastThread = thread
	}
	var jobs []imageJob
	seen := map[string]bool{}
	total := 0
	for _, msg := range t.Messages {
		id := msg.ID
		meta := t.ImageBodies[id]
		for _, img := range meta.Images {
			if !m.imageVisible(t, img) {
				continue
			}
			key := m.imageKey(t, id, meta, img)
			if seen[key] {
				continue
			}
			seen[key] = true
			total++
			if state.loaded[key] != nil || !retry && state.failed[key] != "" {
				continue
			}
			if total > maxThreadImages {
				state.failed[key] = "Conversation preview limit reached"
				continue
			}
			if len(img.Part) > 0 && int64(img.Size) > mailimage.MaxBytes*4/3+4096 {
				state.failed[key] = "Image exceeds the 8 MiB preview limit"
				continue
			}
			if m.live != nil && m.live.offline && !strings.HasPrefix(strings.ToLower(img.Source), "data:") {
				state.failed[key] = "Offline: reconnect to load this image"
				continue
			}
			job := imageJob{Key: key, Image: img, Ref: meta.Ref, Demo: !t.Remote}
			if m.realAccount(t.Account) && len(img.Part) > 0 {
				job.Config = *m.accounts[t.Account].Config
				job.Password = m.live.passwords[m.liveKey(t.Account)]
			}
			delete(state.failed, key)
			state.busy[key] = true
			jobs = append(jobs, job)
		}
	}
	if total == 0 {
		if retry {
			m.notice = "No displayable images in this conversation. Older cached mail may need R to refresh."
		}
		return nil
	}
	if len(jobs) == 0 {
		return nil
	}
	parent := m.background
	if parent == nil {
		parent = context.Background()
	}
	state.ctx, state.cancel = context.WithTimeout(parent, 45*time.Second)
	state.queue, state.thread, state.remaining = jobs, thread, imageDownloadBudget
	var backend mailTransport
	if m.live != nil {
		backend = m.live.backend
	}
	state.load = state.testLoader
	if state.load == nil {
		state.load = newImageLoader(m.credentials, backend)
	}
	m.notice = fmt.Sprintf("Loading %d image(s) · S reading settings", len(jobs))
	return m.nextImage()
}

func (m *model) nextImage() tea.Cmd {
	s := m.images
	if s == nil || len(s.queue) == 0 {
		return nil
	}
	if s.remaining <= 0 || s.ctx.Err() != nil {
		for _, job := range s.queue {
			s.failed[job.Key] = "Image loading limit reached; I retries"
			delete(s.busy, job.Key)
		}
		m.cancelImages()
		m.notice = "Image loading stopped at its time or download limit."
		return nil
	}
	job := s.queue[0]
	s.queue = s.queue[1:]
	job.Limit = min(s.remaining, mailimage.MaxBytes)
	ctx, load, generation := s.ctx, s.load, s.generation
	return func() tea.Msg {
		raster, used, err := load(ctx, job)
		return imageLoadedMsg{key: job.Key, generation: generation, raster: raster, used: used, err: err}
	}
}

func (m model) finishImage(msg imageLoadedMsg) (tea.Model, tea.Cmd) {
	s := m.images
	if s == nil || msg.generation != s.generation || !s.busy[msg.key] {
		return m, nil
	}
	delete(s.busy, msg.key)
	s.remaining -= max(0, msg.used)
	if msg.err != nil || msg.raster == nil {
		text := "Image could not be loaded"
		if msg.err != nil {
			text = imageName(msg.err.Error())
		}
		s.failed[msg.key] = text
	} else {
		if len(s.order) >= 32 {
			delete(s.loaded, s.order[0])
			s.order = s.order[1:]
		}
		s.loaded[msg.key] = &imagePreview{raster: msg.raster, lines: map[string][]string{}}
		s.order = append(s.order, msg.key)
	}
	if len(s.queue) > 0 {
		m.notice = fmt.Sprintf("Loading images · %d remaining", len(s.queue))
		cmd := m.nextImage()
		return m, cmd
	}
	if s.cancel != nil {
		s.cancel()
	}
	s.cancel, s.thread = nil, ""
	m.notice = "Images loaded where available · I retries · S reading settings"
	m.clampScroll()
	return m, nil
}

func (m model) imageBodyLines(t *Thread, index int, msg Message, width int) []string {
	var meta messageImages
	id := msg.ID
	meta = t.ImageBodies[id]
	text := msg.Body
	if len(meta.Images) > 0 && meta.Text != "" {
		text = meta.Text
	}
	byToken := map[string]messageImage{}
	for _, img := range meta.Images {
		byToken[img.token()] = img
	}
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if img, ok := byToken[line]; ok {
			key := m.imageKey(t, id, meta, img)
			label := "[Image: " + imageName(img.Name) + "]"
			if m.images != nil && m.imageVisible(t, img) {
				if preview := m.images.loaded[key]; preview != nil {
					out = append(out, styled(ansi.Truncate("[Image: "+imageName(img.Name)+"]", width, "…"), muted))
					rows := max(4, min(24, m.height-14))
					variant := fmt.Sprintf("%d/%d", width, rows)
					lines, ok := preview.lines[variant]
					if !ok {
						if len(preview.lines) >= 4 {
							preview.lines = map[string][]string{}
						}
						lines = mailimage.Lines(preview.raster, width, rows, violetPane)
						preview.lines[variant] = lines
					}
					out = append(out, lines...)
					continue
				} else if m.images.busy[key] {
					label = "[Image: " + imageName(img.Name) + " · loading…]"
				} else if failure := m.images.failed[key]; failure != "" {
					label = "[Image: " + imageName(img.Name) + " · " + failure + "]"
				}
			}
			for _, wrapped := range strings.Split(ansi.Wrap(label, width, ""), "\n") {
				out = append(out, styled(wrapped, muted))
			}
			continue
		}
		for _, wrapped := range strings.Split(ansi.Wrap(line, width, ""), "\n") {
			out = append(out, styled(wrapped, readerText))
		}
	}
	return out
}

func demoPostcard() image.Image {
	img := image.NewRGBA(image.Rect(0, 0, 96, 54))
	for y := 0; y < 54; y++ {
		for x := 0; x < 96; x++ {
			c := color.RGBA{uint8(60 + y*2), uint8(120 + y), uint8(200 - y), 255}
			if (x-68)*(x-68)+(y-15)*(y-15) < 64 {
				c = color.RGBA{255, 214, 134, 255}
			}
			if y > 31-abs(x-22)/2 && x < 65 {
				c = color.RGBA{44, 76, 106, 255}
			}
			if y > 35 {
				c = color.RGBA{uint8(20 + y/2), uint8(110 + y), uint8(160 + (x+y)%12), 255}
			}
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

func addDemoImage(threads []Thread) {
	embedded := false
	for i := range threads {
		t := &threads[i]
		if t.Account != 0 || t.Folder != "Inbox" || len(t.Messages) == 0 {
			continue
		}
		if t.Subject == "A few photos from the coast" {
			img := messageImage{ID: imageID(demoRemoteImageSource), Source: demoRemoteImageSource, Name: "A photo from the coast (demo)"}
			t.Messages[0].ID = "demo-remote-postcard"
			intro := "Try the image button above this message. This photo behaves like a remote image, but the demo works without a network connection.\n\n"
			t.ImageBodies = map[string]messageImages{"demo-remote-postcard": {Text: intro + img.token() + "\n\n" + t.Messages[0].Body, Images: []messageImage{img}}}
			continue
		}
		if embedded {
			continue
		}
		img := messageImage{ID: imageID("demo:postcard"), Source: "demo:postcard", Name: "A postcard from the coast"}
		t.Messages[0].ID = "demo-postcard"
		t.ImageBodies = map[string]messageImages{"demo-postcard": {Text: img.token() + "\n" + t.Messages[0].Body, Images: []messageImage{img}}}
		embedded = true
	}
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

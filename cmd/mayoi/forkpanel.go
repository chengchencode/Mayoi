package main

// The fork panel: the fork's git workflow as a page in a web view of an
// overlay of the window, rather than the Fork menu's commands in
// terminals. tools/devui served the same page over localhost before
// this. The page calls the bound forkService — mygo.call("ForkPanel.X",
// …) — which streams a script's output back through Page.Eval.

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"sync"
	"syscall"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"mayoi/locales"
)

// forkService is the bound service the panel's page calls, one for the
// app: CallerWindow tells which window's panel a call is from. Bound in
// main as "ForkPanel".
type forkService struct {
	m *mayoi
	// cur is the command a panel is running, when any.
	mu  sync.Mutex
	cur *exec.Cmd
}

// app returns the app of the window a call came from.
func (s *forkService) app(ctx context.Context) *app {
	w := mygo.CallerWindow(ctx)
	for _, d := range s.m.wins {
		if d.f != nil && d.f.win == w {
			return d.a
		}
	}
	return nil
}

// send pushes a chunk of output to the page; eval lands it.
func (s *forkService) send(p *mygo.Page, text string) {
	b, _ := json.Marshal(text)
	p.Eval("forkOut(" + string(b) + ")")
}

func (s *forkService) eval(p *mygo.Page, code string) { _, _ = p.Eval(code) }

// Run runs the script of a fork action in the calling window's
// workspace, streaming the output into its page. Returns at once; the
// page's forkDone ends it.
func (s *forkService) Run(ctx context.Context, cmd, arg, arg2 string) error {
	a := s.app(ctx)
	p := mygo.CallerPage(ctx)
	if a == nil || p == nil {
		return errors.New("no window")
	}
	script, err := forkScriptFor(cmd, arg, arg2)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if s.cur != nil {
		s.mu.Unlock()
		return errors.New(locales.ForkBusy())
	}
	c := exec.Command("bash", "-c", script)
	c.Dir = a.workDir()
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := c.StdoutPipe()
	if err != nil {
		s.mu.Unlock()
		return err
	}
	c.Stderr = c.Stdout
	s.cur = c
	s.mu.Unlock()

	go func() {
		defer func() { s.mu.Lock(); s.cur = nil; s.mu.Unlock() }()
		s.send(p, "$ "+script+"\n")
		if err := c.Start(); err != nil {
			s.send(p, "start: "+err.Error()+"\n")
			s.eval(p, "forkDone(false)")
			return
		}
		buf := make([]byte, 32*1024)
		for {
			n, rerr := out.Read(buf)
			if n > 0 {
				s.send(p, string(buf[:n]))
			}
			if rerr != nil {
				break
			}
		}
		s.eval(p, "forkDone("+map[bool]string{true: "true", false: "false"}[c.Wait() == nil]+")")
	}()
	return nil
}

// Stop kills the command a panel is running, and the group it leads.
func (s *forkService) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur != nil && s.cur.Process != nil {
		_ = syscall.Kill(-s.cur.Process.Pid, syscall.SIGKILL)
	}
}

// PR opens the current branch's compare page — upstream's for base "",
// one of the own fork otherwise — in the browser.
func (s *forkService) PR(ctx context.Context, base string) error {
	a := s.app(ctx)
	if a == nil {
		return errors.New("no window")
	}
	if base != "" && !validBranch(base) {
		return errors.New(locales.BranchInvalidName())
	}
	a.post(func() { a.forkPR(base) })
	return nil
}

// Close dismisses the calling window's panel.
func (s *forkService) Close(ctx context.Context) {
	if a := s.app(ctx); a != nil {
		a.post(func() { a.forkShow = false })
	}
}

// forkPanelToggle shows the panel, or hides it. The web view keeps its
// page while hidden, so the output stays.
func (a *app) forkPanelToggle() {
	if a.forkShow {
		a.forkShow = false
		return
	}
	h, ok := a.host.(*desktop)
	if !ok || h.f == nil || h.f.win == nil {
		return
	}
	if a.forkPanel == nil {
		wv, err := h.f.win.NewWebView(mygo.WebViewOptions{})
		if err != nil {
			a.fail("%s", err)
			return
		}
		a.forkPanel = wv
		wv.Page().LoadHTML(forkPanelPage, "")
	}
	a.forkShow = true
	h.f.win.Invalidate()
}

// forkPanelView builds the panel's overlay while it shows, a centered
// box of the web view that Escape or a click outside dismisses.
func (a *app) forkPanelView(c *ui.Context) {
	if !a.forkShow || a.forkPanel == nil {
		return
	}
	ui.Overlay(c, func() {
		back := a.dialogBack(c, "forkpanel")
		back.Children(func() {
			box := a.floating(c, ui.Column(c).Width(860).MaxWidthPercent(94).
				HeightPercent(72).Margin(titleBarH+8, 0, 0, 0).Radius(10), true)
			box.Role(ui.RoleDialog).Label("Fork Panel")
			box.Children(func() {
				ui.WebView(c, a.forkPanel).Grow(1)
			})
			if box.PressedOutside() || back.OverlayShortcut(0, ui.KeyEscape) {
				a.forkShow = false
			}
		})
	})
}

// forkPanelPage is the panel's page. forkOut(text) appends output and
// forkDone(ok) ends a run; the service evals them.
const forkPanelPage = `<!doctype html>
<meta charset="utf-8">
<title>Fork Panel</title>
<style>
  :root { color-scheme: dark }
  html, body { height: 100%; margin: 0 }
  body { font: 14px/1.5 ui-monospace, monospace; background: #14161a; color: #d7dae0;
         display: flex; flex-direction: column; box-sizing: border-box; padding: 14px 16px }
  h2 { font-size: 12px; text-transform: uppercase; letter-spacing: .08em;
       color: #7f8590; margin: 14px 0 6px }
  h2:first-of-type { margin-top: 0 }
  button, input { font: inherit; border-radius: 8px; border: 1px solid #33383f;
       background: #1e2126; color: inherit; padding: 7px 12px }
  button { cursor: pointer } button:hover { background: #262a31 }
  button:disabled { opacity: .4; cursor: default }
  button.go { background: #1d4ed8; border-color: #1d4ed8 } button.go:hover { background: #2563eb }
  button.danger { border-color: #7f1d1d; color: #fca5a5 }
  .row { display: flex; gap: 8px; flex-wrap: wrap; align-items: center }
  input { min-width: 140px }
  .grow { flex: 1 }
  #bar { display: flex; align-items: center; gap: 10px; margin-bottom: 4px }
  #bar b { font-size: 15px }
  #out { background: #0d0f12; border: 1px solid #262a31; border-radius: 8px;
         padding: 10px 12px; white-space: pre-wrap; word-break: break-all;
         flex: 1; min-height: 120px; overflow: auto; margin-top: 12px; font-size: 13px }
  .tip { color: #7f8590; font-size: 12px }
</style>

<div id="bar">
  <b>Fork Panel</b>
  <span class="grow"></span>
  <button onclick="mygo.call('ForkPanel.Close')">✕ 关闭 (Esc)</button>
</div>

<h2>Daily</h2>
<div class="row">
  <button class="go" onclick="run('sync')">同步上游</button>
  <button onclick="run('status')">刷新状态</button>
</div>

<h2>New change</h2>
<div class="row">
  <input id="feat" placeholder="分支名（自动加 feat/）">
  <input id="base" placeholder="基于（默认 main，填 feat/x 叠加）">
  <button onclick="run('feat', v('feat'), v('base'))">新建分支</button>
  <button onclick="run('push')">推送当前分支</button>
</div>

<h2>Pull request</h2>
<div class="row">
  <input id="prbase" placeholder="base：空=上游 main，填 feat/x=fork 内">
  <button class="go" onclick="mygo.call('ForkPanel.PR', v('prbase'))">提交 PR</button>
  <input id="dep" placeholder="要剥离的依赖（空=已合入上游）">
  <button class="danger" onclick="run('restack', v('dep'))">Rebase 到 main</button>
</div>

<h2>Run</h2>
<div class="row">
  <button class="go" onclick="run('dev')">Dev（热重载）</button>
  <button onclick="run('build')">打包并安装</button>
  <button class="danger" onclick="mygo.call('ForkPanel.Stop')">停止</button>
  <span class="tip">冲突时：Esc 关面板 → 内置终端接着解</span>
</div>

<pre id="out"></pre>

<script>
const out = document.getElementById('out');
const buttons = document.querySelectorAll('button');
const v = (id) => document.getElementById(id).value;
const busy = (b) => buttons.forEach(x => x.disabled = b);

window.forkOut = (text) => {
  out.textContent += text;
  out.scrollTop = out.scrollHeight;
};
window.forkDone = (ok) => {
  out.textContent += ok ? '\n✓ done\n' : '\n✗ failed\n';
  busy(false);
};

async function run(cmd, arg, arg2) {
  busy(true);
  try {
    await mygo.call('ForkPanel.Run', cmd, arg || '', arg2 || '');
  } catch (e) {
    forkOut(String(e) + '\n');
    busy(false);
  }
}
run('status');
</script>
`

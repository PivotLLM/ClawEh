// ClawEh
// License: MIT

package admin

import "html/template"

// Page modes.
const (
	pageForm     = "form"     // the account form
	pageProgress = "progress" // after a valid submission
	pageUsed     = "used"     // a later submission
)

// pageData fills pageTemplate.
type pageData struct {
	Mode      string
	Nonce     string
	Code      string
	Name      string // the product name
	MinLength int
	Username  string // kept after a problem
	Problem   string // one sentence
	Message   string // the progress message
}

// pageTemplate is the one-time account page: self-contained, no external
// resources, inline style and script allowed only by the response's nonce.
var pageTemplate = template.Must(template.New("account").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="referrer" content="no-referrer">
<title>Create the admin account</title>
<style nonce="{{.Nonce}}">
:root{color-scheme:light dark;--bg:#f5f5f3;--fg:#1c1c1a;--muted:#5c5c57;--card:#fff;--line:#cfcfca;--accent:#2152b8;--on-accent:#fff;--bad:#b3261e}
@media (prefers-color-scheme:dark){:root{--bg:#151514;--fg:#ececea;--muted:#a5a59f;--card:#20201e;--line:#3b3b38;--accent:#9db8ff;--on-accent:#10131a;--bad:#ff8a80}}
*{box-sizing:border-box}
body{margin:0;padding:2rem 1rem;background:var(--bg);color:var(--fg);font:16px/1.5 system-ui,-apple-system,"Segoe UI",Roboto,sans-serif}
main{max-width:24rem;margin:0 auto;background:var(--card);border:1px solid var(--line);border-radius:12px;padding:1.5rem}
h1{font-size:1.3rem;line-height:1.3;margin:0 0 .25rem}
p{margin:.5rem 0}
.muted{color:var(--muted)}
.problem{color:var(--bad);font-weight:600}
label{display:block;margin-top:1rem;font-weight:600}
input{display:block;width:100%;padding:.6rem .7rem;margin-top:.25rem;font:inherit;color:inherit;background:transparent;border:1px solid var(--line);border-radius:8px}
input:focus,button:focus,a:focus{outline:2px solid var(--accent);outline-offset:2px}
.hint{font-size:.9rem;color:var(--muted);margin:.25rem 0 0}
button{margin-top:1.5rem;width:100%;padding:.7rem;font:inherit;font-weight:600;color:var(--on-accent);background:var(--accent);border:0;border-radius:8px;cursor:pointer}
a{color:var(--accent);word-break:break-all}
</style>
</head>
<body>
<main>
<h1>Create the admin account</h1>
{{- if eq .Mode "form"}}
<p class="muted">You sign in to {{.Name}} with this account.</p>
{{- if .Problem}}
<p class="problem" role="alert">{{.Problem}}</p>
{{- end}}
<form method="post" action="/">
<input type="hidden" name="code" value="{{.Code}}">
<label for="username">Username</label>
<input id="username" name="username" autocomplete="username" autocapitalize="none" spellcheck="false" required value="{{.Username}}"{{if not .Username}} autofocus{{end}}>
<label for="password">Password</label>
<input id="password" name="password" type="password" autocomplete="new-password" required aria-describedby="password-hint"{{if .Username}} autofocus{{end}}>
<p class="hint" id="password-hint">At least {{.MinLength}} characters.</p>
<label for="repeat">Repeat password</label>
<input id="repeat" name="repeat" type="password" autocomplete="new-password" required>
<button type="submit">Create account</button>
</form>
{{- else if eq .Mode "progress"}}
<p id="message" role="status">{{.Message}}</p>
<p><a id="link" hidden></a></p>
<script nonce="{{.Nonce}}">
(function () {
  var code = {{.Code}};
  var message = document.getElementById("message");
  var link = document.getElementById("link");
  function expired() { message.textContent = "This page has expired."; }
  function poll() {
    fetch("/status?code=" + encodeURIComponent(code), {cache: "no-store"})
      .then(function (r) { if (!r.ok) { throw new Error("status"); } return r.json(); })
      .then(function (s) {
        if (s.message) { message.textContent = s.message; }
        if (s.state === "started") { window.location.replace(s.url); return; }
        if (s.state === "not_answering") { link.href = s.url; link.textContent = s.url; link.hidden = false; return; }
        if (s.state === "failed") { return; }
        setTimeout(poll, 1000);
      })
      .catch(expired);
  }
  poll();
})();
</script>
{{- else}}
<p role="alert">This page has already been used.</p>
{{- end}}
</main>
</body>
</html>
`))

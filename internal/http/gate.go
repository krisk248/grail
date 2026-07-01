package http

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func secs(d time.Duration) string {
	n := int(d.Seconds())
	if n < 1 {
		n = 1
	}
	return strconv.Itoa(n)
}

func humanDur(d time.Duration) string {
	m := int(d.Minutes())
	if m >= 1 {
		return strconv.Itoa(m) + " min"
	}
	return secs(d) + " sec"
}

// gateMW is the site-wide password wall. Every request must carry a valid gate
// cookie, except the gate login endpoint itself. Requests without one get a
// 401 (API/JSON) or the password page (HTML).
func (s *Server) gateMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/gate/login" {
			next.ServeHTTP(w, r)
			return
		}
		if s.Sessions.GateValidate(r.Context(), r) {
			next.ServeHTTP(w, r)
			return
		}
		if wantsJSON(r) {
			writeErr(w, http.StatusUnauthorized, "locked")
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, gatePageHTML)
	})
}

func wantsJSON(r *http.Request) bool {
	if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/admin/api/") {
		return true
	}
	return strings.Contains(r.Header.Get("Accept"), "application/json")
}

type gateLoginReq struct {
	Password string `json:"password"`
}

func (s *Server) postGateLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r, s.TrustedProxies)
	if locked, retry := s.gateLimiter.locked(ip); locked {
		w.Header().Set("Retry-After", secs(retry))
		writeErr(w, http.StatusTooManyRequests, "locked out — try again in "+humanDur(retry))
		return
	}
	var req gateLoginReq
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad body")
		return
	}
	if err := bcrypt.CompareHashAndPassword(s.ViewerPassHash, []byte(req.Password)); err != nil {
		locked := s.gateLimiter.fail(ip)
		time.Sleep(250 * time.Millisecond)
		s.Log.Warn("gate login failed", "ip", ip, "locked", locked)
		if locked {
			writeErr(w, http.StatusTooManyRequests, "too many wrong attempts — locked out for 15 minutes")
			return
		}
		writeErr(w, http.StatusUnauthorized, "wrong password")
		return
	}
	s.gateLimiter.success(ip)
	if err := s.Sessions.GateCreate(r.Context(), w); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Log.Info("gate login ok", "ip", ip)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// gatePageHTML is the "Aperture" gateway: a containment iris over a glowing core.
// The iris spin speed follows Dubai working hours (see the script). On the
// correct cipher the iris opens, a warp flash fires, then it navigates to "/".
const gatePageHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8" />
<meta name="viewport" content="width=device-width, initial-scale=1" />
<meta name="robots" content="noindex,nofollow" />
<title>TTS · Gateway</title>
<style>
  :root{
    --panel:rgba(255,255,255,.04); --line:rgba(150,190,230,.14);
    --ice:#7dd3fc; --teal:#5eead4; --ink:#e8f1ff; --mut:#7f93ad; --deny:#ff5470;
    --spin2:26s; --spin3:60s;
  }
  *{box-sizing:border-box;margin:0;padding:0}
  html,body{height:100%}
  body{
    font-family:"Inter",ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif;
    background:radial-gradient(90% 60% at 50% 42%,#0a1424 0%,#05080f 55%,#03040a 100%);
    color:var(--ink);min-height:100vh;overflow:hidden;
    display:grid;grid-template-rows:1fr auto;place-items:center;
  }
  canvas#stars{position:fixed;inset:0;z-index:0}

  .corner{position:fixed;z-index:6;font-size:11px;font-weight:600;color:var(--mut)}
  .corner.tl{top:24px;left:28px;letter-spacing:.16em;max-width:44vw}
  .corner.tl b{color:var(--ice)}
  .corner.tr{top:24px;right:28px;letter-spacing:.5em;color:var(--ice)}
  .corner.bl{bottom:22px;left:28px;font-family:ui-monospace,monospace;letter-spacing:.14em;
    color:#5f7590;font-size:10.5px}
  .corner.br{bottom:22px;right:28px;letter-spacing:.26em;font-size:10.5px;text-transform:uppercase;
    color:var(--mut)}

  .stagewrap{position:relative;z-index:2;display:grid;place-items:center;padding-top:40px}
  .portal{position:relative;width:min(74vw,400px);aspect-ratio:1;display:grid;place-items:center;
    transition:transform .5s cubic-bezier(.7,0,.2,1),filter .6s}
  .halo{position:absolute;inset:-8%;border-radius:50%;
    background:radial-gradient(circle,rgba(94,234,212,.16),transparent 62%);
    filter:blur(6px);animation:breathe 6s ease-in-out infinite}
  @keyframes breathe{50%{transform:scale(1.06);opacity:.85}}

  .ring{position:absolute;border-radius:50%;pointer-events:none}
  .ring.r1{inset:0;border:1px solid var(--line)}
  .ring.r2{inset:4%;border:1px dashed rgba(125,211,252,.3);animation:spin var(--spin2) linear infinite}
  .ring.r3{inset:-5%;border:1px solid rgba(125,211,252,.12)}
  .ticks{position:absolute;inset:-5%;border-radius:50%;animation:spin var(--spin3) linear infinite reverse}
  .ticks i{position:absolute;top:0;left:50%;width:2px;height:11px;background:rgba(125,211,252,.5);
    transform-origin:1px 210px;border-radius:2px}
  @keyframes spin{to{transform:rotate(360deg)}}
  body.rest .ring.r2,body.rest .ticks,body.rest .core::after{animation-play-state:paused}

  .core{position:absolute;inset:20%;border-radius:50%;
    background:radial-gradient(circle at 50% 45%,#eafff9 0%,#5eead4 16%,#1f8fd6 46%,#123a7a 74%,#050b18 100%);
    box-shadow:0 0 60px rgba(94,234,212,.35),inset 0 0 40px rgba(3,10,25,.8);
    filter:saturate(1.05);animation:pulseCore 5s ease-in-out infinite}
  .core::after{content:"";position:absolute;inset:0;border-radius:50%;mix-blend-mode:screen;
    background:conic-gradient(from 0deg,transparent,rgba(255,255,255,.22),transparent 30%);
    animation:spin var(--spin2) linear infinite}
  @keyframes pulseCore{50%{filter:brightness(1.12) saturate(1.15)}}

  .iris{position:absolute;inset:6%;border-radius:50%;overflow:hidden;z-index:3;
    box-shadow:inset 0 0 34px rgba(94,234,212,.18),0 0 0 1px rgba(125,211,252,.35),
      0 0 22px rgba(94,234,212,.14)}
  .petal{position:absolute;inset:0;transform-origin:50% 50%;
    transition:transform 1.15s cubic-bezier(.66,0,.18,1),opacity .9s ease}
  .petal::before{content:"";position:absolute;inset:-1px;
    background:linear-gradient(150deg,#2b3a54 0%,#18253a 46%,#0d1626 100%);
    border:1px solid rgba(125,211,252,.32);
    box-shadow:inset 0 0 26px rgba(0,0,0,.55),inset 2px 2px 8px rgba(160,200,240,.12);
    clip-path:polygon(50% 52%,24% -6%,76% -6%)}
  .center-dot{position:absolute;top:50%;left:50%;width:10px;height:10px;transform:translate(-50%,-50%);
    border-radius:50%;background:var(--teal);box-shadow:0 0 16px var(--teal);z-index:4;
    transition:opacity .5s;animation:pulseCore 3s ease-in-out infinite}

  .wrap.granted .petal{transform:rotate(var(--a)) translateY(-74%) scale(1.05);opacity:0}
  .wrap.granted .center-dot{opacity:0}
  .wrap.granted .portal{filter:brightness(1.25)}
  .wrap.granted .core{animation:flare 1.1s ease forwards}
  @keyframes flare{60%{box-shadow:0 0 120px rgba(94,234,212,.7)}100%{box-shadow:0 0 80px rgba(94,234,212,.45)}}

  .wrap.deny .portal{animation:clench .5s}
  .wrap.deny .ring.r1,.wrap.deny .ring.r3{border-color:rgba(255,84,112,.6)}
  .wrap.deny .core{filter:hue-rotate(-120deg) brightness(1.05)}
  @keyframes clench{35%{transform:scale(.955)}70%{transform:scale(1.01)}}

  .verdict{position:absolute;z-index:5;text-align:center;pointer-events:none;
    font-size:15px;letter-spacing:.36em;font-weight:600;opacity:0;transition:opacity .5s}
  .verdict .big{display:block;font-size:20px;letter-spacing:.3em;margin-top:4px}
  .wrap.granted .verdict{opacity:1;color:#eafff9;text-shadow:0 0 24px var(--teal)}

  .auth{position:relative;z-index:4;margin:34px auto 60px;width:min(90vw,430px);text-align:center}
  .auth .kicker{font-size:11px;letter-spacing:.44em;color:var(--mut);margin-bottom:14px;font-weight:600}
  .field{display:flex;align-items:center;gap:2px;background:var(--panel);
    border:1px solid var(--line);border-radius:14px;padding:6px 6px 6px 18px;
    backdrop-filter:blur(10px);transition:border-color .25s,box-shadow .25s}
  .field:focus-within{border-color:rgba(125,211,252,.55);box-shadow:0 0 0 3px rgba(125,211,252,.1)}
  .field .glyph{color:var(--ice);font-size:14px;opacity:.8}
  input{flex:1;background:transparent;border:0;outline:none;color:var(--ink);
    font-size:15px;letter-spacing:.14em;padding:12px}
  input::placeholder{color:#5b6f89;letter-spacing:.24em;font-size:13px}
  button{border:0;cursor:pointer;border-radius:10px;padding:12px 18px;font-weight:700;
    letter-spacing:.14em;font-size:12px;color:#04222b;
    background:linear-gradient(150deg,var(--teal),var(--ice));
    box-shadow:0 4px 20px rgba(94,234,212,.28);transition:transform .1s,filter .2s}
  button:hover{filter:brightness(1.06)}button:active{transform:translateY(1px)}
  .status{height:16px;margin-top:12px;font-size:11px;letter-spacing:.28em;color:var(--deny);opacity:0;
    transition:opacity .2s;font-family:ui-monospace,monospace}
  .wrap.deny .status{opacity:1}
  .wrap.deny .field{border-color:rgba(255,84,112,.6);animation:shake .45s}
  .wrap.granted .auth{opacity:0;transform:translateY(14px);transition:.5s;pointer-events:none}
  @keyframes shake{20%{transform:translateX(-7px)}45%{transform:translateX(7px)}70%{transform:translateX(-4px)}}

  .warp{position:fixed;inset:0;z-index:20;pointer-events:none;opacity:0;transition:opacity .55s ease;
    background:radial-gradient(circle at 50% 45%,rgba(210,255,247,.95),rgba(94,234,212,.5) 30%,rgba(4,6,12,0) 66%)}
  body.blast .warp{opacity:1}
</style>
</head>
<body>
<canvas id="stars"></canvas>
<div class="warp"></div>
<div class="corner tl"><b>Total Technologies and Solutions</b> FZ-LLC</div>
<div class="corner tr">GATEWAY</div>
<div class="corner bl">base@ttsdubai.com</div>
<div class="corner br">Your gateway into work</div>

<div class="wrap" id="wrap">
  <div class="stagewrap">
    <div class="portal" id="portal">
      <span class="halo"></span>
      <span class="ring r3"></span>
      <span class="ring r1"></span>
      <span class="ring r2"></span>
      <span class="ticks" id="ticks"></span>
      <span class="core"></span>
      <span class="iris" id="iris"></span>
      <span class="center-dot"></span>
      <span class="verdict">CLEARANCE<span class="big">GRANTED</span></span>
    </div>
  </div>

  <form class="auth" id="form" autocomplete="off">
    <div class="kicker">◇ PRESENT YOUR CIPHER TO PASS ◇</div>
    <div class="field">
      <span class="glyph">⬡</span>
      <input id="pw" type="password" placeholder="ACCESS CIPHER" autofocus />
      <button type="submit">AUTHENTICATE</button>
    </div>
    <div class="status" id="status"></div>
  </form>
</div>

<script>
  var wrap=document.getElementById('wrap'),form=document.getElementById('form'),
      pw=document.getElementById('pw'),statusEl=document.getElementById('status');

  // iris petals
  var iris=document.getElementById('iris'),N=8;
  for(var i=0;i<N;i++){var p=document.createElement('span');p.className='petal';
    var a=(i*360/N)+'deg';p.style.setProperty('--a',a);p.style.transform='rotate('+a+')';
    iris.appendChild(p);}
  // ring ticks
  var ticks=document.getElementById('ticks');
  for(var t=0;t<36;t++){var ti=document.createElement('i');ti.style.transform='rotate('+(t*10)+'deg)';ticks.appendChild(ti);}

  // Dubai working-hours spin: 06-10 slow, 10-18 fast, else rest (paused).
  var now=new Date(),dubai=(now.getUTCHours()+now.getUTCMinutes()/60+4)%24,root=document.documentElement;
  if(dubai>=6&&dubai<10){root.style.setProperty('--spin2','70s');root.style.setProperty('--spin3','150s');}
  else if(dubai>=10&&dubai<18){root.style.setProperty('--spin2','14s');root.style.setProperty('--spin3','34s');}
  else{document.body.classList.add('rest');}

  // starfield
  var cv=document.getElementById('stars'),cx=cv.getContext('2d'),W,H,stars;
  function resize(){W=cv.width=innerWidth;H=cv.height=innerHeight;
    stars=[];for(var k=0;k<150;k++)stars.push({x:Math.random()*W,y:Math.random()*H,
      z:Math.random(),s:Math.random()*1.4+.2});}
  resize();addEventListener('resize',resize);
  (function draw(){cx.clearRect(0,0,W,H);
    for(var j=0;j<stars.length;j++){var st=stars[j];st.y+=st.z*.12;if(st.y>H)st.y=0;
      cx.globalAlpha=.25+st.z*.55;cx.fillStyle=st.z>.7?'#7dd3fc':'#9fb4cc';
      cx.fillRect(st.x,st.y,st.s,st.s);}
    cx.globalAlpha=1;requestAnimationFrame(draw);})();

  form.addEventListener('submit',function(e){
    e.preventDefault();
    if(wrap.classList.contains('granted'))return;
    statusEl.textContent='';
    fetch('/gate/login',{method:'POST',headers:{'Content-Type':'application/json'},
      body:JSON.stringify({password:pw.value})}).then(function(r){
        if(r.ok){
          wrap.classList.add('granted');
          setTimeout(function(){document.body.classList.add('blast');},950);
          setTimeout(function(){location.href='/';},1550);
          return null;
        }
        return r.json().catch(function(){return {};}).then(function(j){
          statusEl.textContent=(r.status===429)?('⨯ '+((j&&j.error)||'LOCKED OUT'))
            :'⨯ CIPHER REJECTED — REALIGN AND RETRY';
          wrap.classList.remove('deny');void wrap.offsetWidth;wrap.classList.add('deny');pw.select();
          setTimeout(function(){wrap.classList.remove('deny');},1500);
        });
      }).catch(function(){
        statusEl.textContent='⨯ LINK FAILURE';
        wrap.classList.add('deny');setTimeout(function(){wrap.classList.remove('deny');},1500);
      });
  });
</script>
</body>
</html>`

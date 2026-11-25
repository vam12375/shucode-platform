// Signal 背景着色器（specs/device-login-pages.md）。
//
// 与设备登录页 server/internal/apiapp/assets/device-auth/auth.js 中的 VS / FS 必须逐字一致：
// 两端是同一视觉，tests/signal-shader.test.mjs 会比对两份源码防止漂移。
// 信号源：mixV=0 时为两层流动值噪声；有视频时 mixV 渐变到 1，采样视频纹理。

export const SIGNAL_VERTEX_SHADER = "attribute vec2 p;void main(){gl_Position=vec4(p,0,1);}";

export const SIGNAL_FRAGMENT_SHADER = [
  "precision highp float;",
  "uniform sampler2D t;uniform vec2 res,vid,mouse,click;uniform float time,px,r,clickT,dpr,mixV;",
  "float b2(vec2 a){a=floor(a);return fract(a.x/2.+a.y*a.y*.75);}",
  "float b4(vec2 a){return b2(.5*a)*.25+b2(a);}",
  "float b8(vec2 a){return b4(.5*a)*.25+b2(a);}",
  "float hash(vec2 p){return fract(sin(dot(p,vec2(41.3,289.1)))*43758.5);}",
  "float noise(vec2 p){vec2 i=floor(p),f=fract(p);f=f*f*(3.-2.*f);return mix(mix(hash(i),hash(i+vec2(1,0)),f.x),mix(hash(i+vec2(0,1)),hash(i+1.),f.x),f.y);}",
  "vec2 cover(vec2 u){return u;}",
  "vec3 src(vec2 u){vec2 a=vec2((u.x-.5)*res.x/res.y+.5,u.y);float n=noise(a*4.+time*.15)*.6+noise(a*11.-time*.2)*.4;vec3 nc=vec3(n);if(mixV<.001)return nc;float sr=res.x/res.y,vr=vid.x/vid.y;vec2 s=sr>vr?vec2(1.,vr/sr):vec2(sr/vr,1.);vec2 v=(u-.5)*s+.5;return mix(nc,texture2D(t,vec2(v.x,1.-v.y)).rgb,mixV);}",
  "void main(){",
  "  vec2 fc=gl_FragCoord.xy;",
  "  float cd=distance(fc,click);float age=time-clickT;",
  "  float wave=sin(cd*.045-age*9.)*exp(-age*1.6)*(1.-smoothstep(age*620.-60.,age*620.+60.,cd))*step(0.,age);",
  "  vec2 dir=normalize(fc-click+.001);",
  "  vec2 cell=floor((fc+dir*wave*18.)/px)*px+px*.5;",
  "  vec2 u=cover(cell/res);",
  "  u+=vec2(noise(u*3.+time*.1),noise(u*3.-time*.1)-.5)*.004;",
  "  float l=dot(src(u),vec3(.299,.587,.114));",
  "  l=pow(smoothstep(.06,.95,l),1.6)*.92;",
  "  float on=step(b8(fc/px),l);",
  "  vec3 ink=vec3(.047,.047,.051),paper=vec3(.925,.902,.847),sig=vec3(1.,.415,.17);",
  "  vec3 dith=mix(ink,paper,on);",
  "  float d=distance(fc,mouse);",
  "  float lens=1.-smoothstep(r-1.5*dpr,r+1.5*dpr,d);",
  "  vec2 off=(fc-mouse)/res;float k=1.-.35*(1.-d*d/max(r*r,1.));",
  "  vec2 lc=cover((mouse+(fc-mouse)*k)/res);",
  "  vec3 raw=vec3(src(lc+off*.012).r,src(lc).g,src(lc-off*.012).b);",
  "  raw=pow(raw,vec3(.92))*1.08;",
  "  float ring=smoothstep(2.*dpr,0.,abs(d-r))*.9;",
  "  float halo=smoothstep(r*1.9,r,d)*(1.-lens);",
  "  dith=mix(dith,mix(ink,sig,on),halo*.85);",
  "  vec3 col=mix(dith,raw,lens);",
  "  col=mix(col,sig,ring);",
  "  col*=.94+.06*sin(fc.y*1.6/dpr);",
  "  gl_FragColor=vec4(col,1.);",
  "}",
].join("\n");

export const SIGNAL_UNIFORMS = ["t", "res", "vid", "mouse", "click", "time", "px", "r", "clickT", "dpr", "mixV"] as const;

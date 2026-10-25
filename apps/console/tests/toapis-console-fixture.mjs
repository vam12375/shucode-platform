// 本地浏览器验收夹具：请求只命中内存目录，不读取真实供应商凭据或调用生成接口。
import { createServer } from "vite";
import { fileURLToPath } from "node:url";

const root = fileURLToPath(new URL("../", import.meta.url));
const first = {
  id: "ark-existing", name: "已有 Ark 720p", kind: "video", providerId: "ark",
  upstreamModel: "doubao-seedance-2-0-mini-260615", adapter: "ark", resolutions: ["720p"],
  minDuration: 4, maxDuration: 15, costMicro: 100000, priceMicro: 200000,
  generateAudio: false, priceVersion: "fixture-v1",
};
const arkImage = { ...first, id: "ark-image-existing", name: "已有 Ark 图片", kind: "image", upstreamModel: "doubao-seedream-5-0-pro-260628", resolutions: [] };
const projects = ["toapis", "ark"].map((adapter) => ({
  id: `${adapter}-image-project`, teamId: "fixture-team", version: 7, status: "reconciliation_required",
  brief: { product: `${adapter === "toapis" ? "ToAPIs" : "Ark"} 图片核对夹具`, imageModel: "fixture-image-sku", videoModel: "", resolution: "2K", duration: 0 },
  ...(adapter === "toapis" ? { canvas: { kind: "image", modelId: "fixture-image-sku" } } : {}),
  models: [{ id: "fixture-image-sku", kind: "image", adapter, upstreamModel: adapter === "toapis" ? "gpt-image-2" : "doubao-seedream-5-0-pro-260628" }],
  shots: [{ status: "reconciliation_required", stage: "image", attempt: 1, pendingChargeMicro: 100000, chargedMicro: 0 }],
  updatedAt: "2026-10-25T01:00:00Z", quoteMicro: 100000,
}));
const html = `<!doctype html><html lang="zh-CN"><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>ToAPIs 媒体目录验收</title></head><body><div id="root"></div>
<script>
window.__fixture = {models:JSON.parse(localStorage.getItem('toapis-catalog-fixture') || 'null') || ${JSON.stringify([first, arkImage])},projects:${JSON.stringify(projects)},calls:[]};
window.fetch = async (input, init={}) => {
  const url = new URL(String(input), location.href);
  if (url.origin !== location.origin || !url.pathname.startsWith('/console/api/')) throw new Error('fixture blocks external network');
  const path=url.pathname, method=init.method || 'GET';
  window.__fixture.calls.push({path,search:url.search,method,...(init.body?{body:JSON.parse(init.body)}:{})});
  let data;
  if(path.endsWith('/media/models')) {
    if(method==='PUT') {window.__fixture.models=JSON.parse(init.body).map(model=>({...model,aspectRatios:model.kind==='image'&&model.adapter==='toapis'?['1:1','3:2','2:3','4:3','3:4','5:4','4:5','16:9','9:16','21:9',...(model.upstreamModel==='gpt-image-2'?['2:1','1:2','9:21']:[])]:['auto','16:9','4:3','1:1','3:4','9:16','21:9']}));localStorage.setItem('toapis-catalog-fixture',JSON.stringify(window.__fixture.models));}
    data=window.__fixture.models;
  } else if(path.endsWith('/providers')) data={providers:[{id:'ark',name:'Ark 测试供应商',baseUrl:'https://ark.example/api/v3',enabled:true},{id:'toapis',name:'ToAPIs 测试供应商',baseUrl:'https://toapis.com/v1',enabled:true}]};
  else if(path.includes('/providers/') && path.endsWith('/models')) data={models:url.searchParams.get('type')==='video'?['seedance-2','seedance-2-fast','seedance-2-mini','MiniMax-H3','MiniMax-H3-Max','MiniMax-H3-Max-Turbo']:url.searchParams.get('type')==='image'?['gpt-image-2','gpt-image-2.5-flare','gpt-image-2.5-sunburst']:['text-fixture']};
  else if(path.endsWith('/media/reference-pricing')) data={snapAnyImportMicro:0,analysisMicro:0};
  else if(path.endsWith('/media/source-parser')) data={configured:false,provider:'snapany',maxBytes:1048576,maxSeconds:90,source:'none',environmentConfigured:false,canConfigure:false};
  else if(path.endsWith('/media/projects')) data=window.__fixture.projects;
  else if(path.endsWith('/reconcile')&&method==='POST') {const project=window.__fixture.projects.find(project=>path.includes('/'+project.id+'/'));const body=JSON.parse(init.body);project.shots[body.shot].status=body.outcome==='image_task'?'image_running':body.outcome==='image_result'?'image_generated':'failed';data=project;}
  else throw new Error('unsupported fixture API');
  return new Response(JSON.stringify({code:0,data}),{headers:{'content-type':'application/json'}});
};
</script>
<script type="module">
import React from 'react';import {createRoot} from 'react-dom/client';
import {MediaModelsPage} from '/src/pages/MediaModels.tsx';import {ToastHost} from '/src/components/Feedback.tsx';import '/src/styles.css';
createRoot(document.getElementById('root')).render(React.createElement(React.Fragment,null,React.createElement('main',{style:{maxWidth:1280,margin:'24px auto',padding:'0 20px'}},React.createElement(MediaModelsPage)),React.createElement(ToastHost)));
</script></body></html>`;
const server = await createServer({
  root, configFile: root + "vite.config.ts",
  server: { host: "127.0.0.1", port: 5194, strictPort: true },
  plugins: [{ name: "toapis-console-fixture", configureServer(server) {
    server.middlewares.use(async (req, res, next) => {
      if (!req.url?.startsWith("/console/toapis-fixture")) return next();
      res.setHeader("content-type", "text/html");
      res.end(await server.transformIndexHtml(req.url, html));
    });
  } }],
});
await server.listen();
process.stdout.write("http://127.0.0.1:5194/console/toapis-fixture\n");

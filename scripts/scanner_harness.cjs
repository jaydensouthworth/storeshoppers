// Deterministic media/canvas harness. It exercises lifecycle code, not real optics.
const scanner = require('../web/static/scanner.js');
const ZXing = require('../web/static/zxing-library-0.23.0.min.js');
function deferred(){let resolve,reject;const promise=new Promise((a,b)=>{resolve=a;reject=b;});return {promise,resolve,reject};}
class Element {
 constructor(){this.listeners=new Map();this.dataset={};this.hidden=false;this.value='';this.isConnected=true;this.textContent='';this.disabled=false;this.events=[];}
 addEventListener(type,fn){if(!this.listeners.has(type))this.listeners.set(type,new Set());this.listeners.get(type).add(fn);}
 removeEventListener(type,fn){this.listeners.get(type)?.delete(fn);}
 dispatchEvent(event){this.events.push(event.type);for(const fn of this.listeners.get(event.type)||[])fn(event);return true;}
 emit(type,detail={}){return this.dispatchEvent({type,detail,target:this});}
 focus(){this.focused=true;}
 contains(el){return el===this;}
}
function harness(options={}){
 const root=new Element(),form=new Element(),input=new Element(),source=new Element(),format=new Element();source.value='manual';format.value='Code128';
 const parts=Object.fromEntries(['start','stop','photo','video','status'].map(k=>[k,new Element()]));
 root.querySelector=s=>parts[s.match(/data-scan-(.*?)\]/)?.[1]];root.closest=()=>form;
 form.querySelector=s=>s==='[data-handheld-scanner]'?root:{'[name="code"]':input,'[name="source"]':source,'[name="format"]':format}[s];
 input.closest=()=>form;
 let pixels=options.pixels||{width:300,height:120,data:new Uint8ClampedArray(300*120*4).fill(255)};
 const stats={requested:0,stopped:0,played:0,paused:0,draws:0,submitted:0,urls:[],revoked:[],detects:0,nativeConstructed:0};
 const track={stop(){stats.stopped++;},onended:null};
 const media={getTracks:()=>[track]};
 const video=parts.video;video.videoWidth=pixels.width;video.videoHeight=pixels.height;video.play=async()=>{stats.played++;if(options.playError)throw options.playError;};video.pause=()=>stats.paused++;
 const timers=new Map();let timerID=0;
 const canvas={width:0,height:0,getContext(){return {fillRect(){},drawImage(el){stats.draws++;stats.lastDraw=el;},getImageData(){if(options.pixelError)throw options.pixelError;return pixels;}};}};
 const doc=new Element();doc.readyState='complete';doc.hidden=false;doc.createElement=()=>canvas;doc.querySelectorAll=()=>root.isConnected?[root]:[];doc.documentElement=new Element();
 const env=new Element();Object.assign(env,{document:doc,isSecureContext:true,Event:class {constructor(type){this.type=type;}},ZXing, navigator:{mediaDevices:{getUserMedia:async constraints=>{stats.requested++;stats.constraints=constraints;if(options.permission)return options.permission.promise;if(options.permissionError)throw options.permissionError;return media;}}}, setTimeout(fn,ms){timers.set(++timerID,{fn,ms});return timerID;},clearTimeout(id){timers.delete(id);}, URL:{createObjectURL(){const url='blob:local-'+stats.urls.length;stats.urls.push(url);return url;},revokeObjectURL(url){stats.revoked.push(url);}}, Image:class{constructor(){this.naturalWidth=pixels.width;this.naturalHeight=pixels.height;}set src(value){this._src=value;if(value){queueMicrotask(()=>options.imageError?this.onerror?.():this.onload?.());}}}});
 if(options.native!==false)env.BarcodeDetector=class{static async getSupportedFormats(){return options.formats||['code_128','qr_code'];}constructor(config){stats.nativeConstructed++;stats.nativeFormats=config.formats;}async detect(){stats.detects++;if(options.detectPromise)return options.detectPromise.promise;if(options.detectError)throw options.detectError;return options.results||[];}};
 form.submit=form.requestSubmit=()=>stats.submitted++;
 const installation=options.install?scanner.install(env):null;
 const instance=installation?[...installation.scanners.values()][0]:scanner.createScanner(root,env);
 async function tick(){const entry=[...timers].find(([,v])=>v.ms===0||v.ms===scanner.LIMITS.interval);if(entry){timers.delete(entry[0]);await entry[1].fn();}await flush();}
 async function photoFile(file){parts.photo.files=[file];await instance.choosePhoto();}
 return {root,form,input,source,format,parts,video,canvas,doc,env,stats,media,track,timers,instance,installation,tick,photoFile,setPixels(value){pixels=value;video.videoWidth=value.width;video.videoHeight=value.height;}};
}
async function flush(){for(let i=0;i<8;i++)await Promise.resolve();}
function pngHeader(width=300,height=120){const b=new Uint8Array(24);b.set([137,80,78,71,13,10,26,10],0);b.set([73,72,68,82],12);const v=new DataView(b.buffer);v.setUint32(16,width);v.setUint32(20,height);return b.buffer;}
module.exports={harness,deferred,flush,pngHeader,Element};

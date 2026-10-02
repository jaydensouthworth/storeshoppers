// Independent decoder checks: input is a PNG round-trip's actual grayscale pixels.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const z = require('../web/static/zxing-library-0.23.0.min.js');
const scanner = require('../web/static/scanner.js');
const {harness,pngHeader}=require('./scanner_harness.cjs');
const fixtures = JSON.parse(fs.readFileSync(0, 'utf8'));
function rotate(gray,w,h){const r=new Uint8ClampedArray(gray.length);for(let y=0;y<h;y++)for(let x=0;x<w;x++)r[x*h+h-y-1]=gray[y*w+x];return r;}
function independent(gray,w,h){
 const hints=new Map([[z.DecodeHintType.POSSIBLE_FORMATS,[z.BarcodeFormat.CODE_128]],[z.DecodeHintType.TRY_HARDER,true]]);
 for(let turn=0;turn<2;turn++){
  try {const reader=new z.Code128Reader();return reader.decode(new z.BinaryBitmap(new z.HybridBinarizer(new z.RGBLuminanceSource(gray,w,h))),hints).getText();}catch(e){if(!/NotFound|Checksum|Format/.test(e.getKind?.()||e.constructor.kind||e.name))throw e;}
  gray=rotate(gray,w,h);[w,h]=[h,w];
 }
 return null;
}
async function main(){
for(const f of fixtures){
 const gray=Uint8ClampedArray.from(Buffer.from(f.pixels,'base64'));
 assert.equal(gray.length,f.width*f.height,f.name);
 const data=new Uint8ClampedArray(gray.length*4);gray.forEach((v,i)=>{data[i*4]=data[i*4+1]=data[i*4+2]=v;data[i*4+3]=255;});
 if(f.mode==='qr'){const result=new z.QRCodeReader().decode(new z.BinaryBitmap(new z.HybridBinarizer(new z.RGBLuminanceSource(gray,f.width,f.height))));assert.equal(result.getText(),f.code,`${f.name} independent QR`);continue;}
 const full=independent(gray,f.width,f.height);
 const found=scanner.decodePixels({width:f.width,height:f.height,data},z).map(x=>x.rawValue);
 if(f.mode==='exact'){assert.equal(full,f.code,`${f.name} independent`);assert.deepEqual(found,[f.code],`${f.name} app pixel path`);}
 if(f.mode==='none'){assert.equal(full,null,`${f.name} independent negative`);assert.deepEqual(found,[],`${f.name} app negative`);}
 if(f.mode==='safe'){assert.ok(full===null||full===f.code,`${f.name}: no false product`);assert.ok(found.every(x=>x===f.code),`${f.name}: app no false product`);}
 if(f.mode==='multiple'){assert.equal(found.length,2,`${f.name}: app refuses ambiguity`);}
 const camera=harness({native:false,pixels:{width:f.width,height:f.height,data}});
 await camera.instance.startCamera();await camera.tick();
 assert.equal(camera.stats.draws,1,`${f.name} production video-to-canvas call`);
 assert.equal(camera.stats.lastDraw,camera.video);
 if(f.mode==='exact'){assert.equal(camera.input.value,f.code,`${f.name} camera fallback pipeline`);assert.equal(camera.source.value,'camera');assert.equal(camera.stats.stopped,1);}
 else if(f.mode!=='safe') assert.equal(camera.input.value,'',`${f.name} no invalid recognition`);
 assert.equal(camera.stats.submitted,0,`${f.name} recognition never picks`);
 camera.instance.stop();
 if(f.mode==='exact'){const photo=harness({native:false,pixels:{width:f.width,height:f.height,data}});await photo.photoFile({type:'image/png',size:24,arrayBuffer:async()=>pngHeader(f.width,f.height)});assert.equal(photo.input.value,f.code,`${f.name} photo fallback pipeline`);assert.equal(photo.source.value,'photo');assert.deepEqual(photo.stats.revoked,photo.stats.urls);assert.equal(photo.stats.submitted,0);}
}
console.log(`${fixtures.length} actual image fixtures independently decoded with pinned ZXing; production pixel decoder checked too`);

}
main().catch(error=>{console.error(error);process.exitCode=1;});

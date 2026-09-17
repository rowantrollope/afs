#!/usr/bin/env python3
"""Host the unchanged original history drawer against a disposable new AFS API."""
import argparse
import contextlib
import hashlib
import json
import os
import pathlib
import shutil
import socket
import subprocess
import time
import urllib.parse
import urllib.request

import compare


def free_port():
    with socket.socket() as listener:
        listener.bind(("127.0.0.1",0))
        return listener.getsockname()[1]


def wait_for_api(child, ready):
    deadline=time.monotonic()+10
    while time.monotonic()<deadline:
        if child.poll() is not None:
            raise RuntimeError("disposable API fixture exited; inspect api.log")
        if ready.is_file():
            return json.loads(ready.read_text())["url"]
        time.sleep(.02)
    raise RuntimeError("disposable API fixture readiness timed out")


def verify_actions(api_base):
    base=api_base+"/v1/databases/local/workspaces/history-ui"
    def get(route,**query):
        with urllib.request.urlopen(base+route+"?"+urllib.parse.urlencode(query),timeout=10) as response:
            return json.load(response)
    result={}
    for path,want,ordinal in (("/story.txt","draft revision 2\n",56),("/deleted.txt","recover this deleted content\n",3)):
        live=get("/files/content",path=path,view="working-copy")
        history=get("/files/history",path=path,direction="desc",limit=1)
        latest=history["lineages"][0]["versions"][0]
        result[path]={"live_content":live.get("content"),"expected_content_matches":live.get("content")==want,"latest_ordinal":latest["ordinal"],"expected_ordinal_matches":latest["ordinal"]==ordinal,"latest_source":latest.get("source"),"lineage_state":history["lineages"][0]["state"]}
    activity=get("/changes",path="/activity-only.txt",direction="desc",limit=25)
    result["activity_while_off"]={"entries":activity.get("entries",[])}
    attributed=get("/changes",path="/attributed.txt",direction="desc",limit=25)
    result["attributed_activity"]={"entries":attributed.get("entries",[])}
    history=get("/files/history",path="/attributed.txt",direction="desc",limit=1)
    result["attributed_version"]=history["lineages"][0]["versions"][0]
    return result


@contextlib.contextmanager
def process(args, log, **kwargs):
    with log.open("wb") as output:
        child = subprocess.Popen(args,stdout=output,stderr=output,**kwargs)
        try:
            yield child
        finally:
            child.terminate()
            try:
                child.wait(timeout=10)
            except subprocess.TimeoutExpired:
                child.kill()
                child.wait()


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--original",required=True,type=pathlib.Path)
    parser.add_argument("--original-ref",default="1ff1fa0589b0e01891f5137fe0ed6fb7cf3d5d2e")
    parser.add_argument("--output",required=True,type=pathlib.Path)
    parser.add_argument("--node-modules",type=pathlib.Path,help="read-only original UI dependencies, default ORIGINAL/ui/node_modules")
    parser.add_argument("--redis-server",default=shutil.which("redis-server"),type=pathlib.Path)
    parser.add_argument("--duration",type=int,default=1800,help="maximum server lifetime in seconds")
    parser.add_argument("--component-test",action="store_true",help="run supplemental original-component tests in jsdom against real API/Redis, then exit; this is not a browser test")
    args=parser.parse_args()
    if args.duration <= 0: parser.error("--duration must be positive")
    output=args.output.resolve()
    if output.exists(): parser.error("--output must be a new directory")
    output.mkdir(parents=True)
    original=args.original.resolve()
    revision=compare.command(["git","-C",str(original),"rev-parse",args.original_ref])
    original_copy,new_copy=output/"original-source",output/"new-source"
    compare.snapshot_original(original,revision,original_copy)
    compare.snapshot_current(new_copy)
    new_source_hash=compare.tree_hash(new_copy)
    new_production_hash=compare.tree_hash(new_copy,True)
    ui=original_copy/"ui"
    modules=(args.node_modules or original/"ui/node_modules").resolve()
    if not (modules/"vite/bin/vite.js").is_file(): parser.error("original UI dependencies must be installed before this read-only comparison")
    (ui/"node_modules").symlink_to(modules,target_is_directory=True)
    shutil.copy2(compare.HERE/"ui_host.tsx.in",ui/"history-parity.tsx")
    (ui/"history-parity.html").write_text('<!doctype html><html><head><meta charset="utf-8"><title>Original history drawer / new AFS</title></head><body><div id="root"></div><script type="module" src="/history-parity.tsx"></script></body></html>')
    (ui/"history-parity.config.mjs").write_text('import {defineConfig} from "vite"; import react from "@vitejs/plugin-react"; export default defineConfig({plugins:[react()],cacheDir:"./.history-parity-cache",server:{host:"127.0.0.1",strictPort:true}});')
    shutil.copy2(compare.HERE/"ui_component.test.tsx.in",ui/"history-parity.test.tsx")
    (ui/"history-parity-test.config.mjs").write_text('import {defineConfig} from "vitest/config"; import react from "@vitejs/plugin-react"; export default defineConfig({plugins:[react()],resolve:{dedupe:["react","react-dom"]},cacheDir:"./.history-parity-test-cache",test:{environment:"jsdom",include:["history-parity.test.tsx"],testTimeout:15000,server:{deps:{inline:[/@redis-ui\\//,/styled-components/]}}}});')
    env=os.environ.copy()
    for key in ("AFS_REDIS_URL","AFS_REDIS_PASSWORD","AFS_E2E_BINARY"): env.pop(key,None)
    env["AFS_HISTORY_COMPARISON_ISOLATED"]="1"
    env["GOCACHE"]=env.get("GOCACHE","/private/tmp/afs-history-go-cache")
    env["GOMODCACHE"]=compare.command(["go","env","GOMODCACHE"])
    env["HOME"]=str(output/"private-home")
    pathlib.Path(env["HOME"]).mkdir()
    seed_source=new_copy/".ui-comparison"
    seed_source.mkdir()
    shutil.copy2(compare.HERE/"ui_seed.go.in",seed_source/"main.go")
    server_source=new_copy/".ui-server"
    server_source.mkdir()
    shutil.copy2(compare.HERE/"ui_server.go.in",server_source/"main.go")
    for package,binary in (("./.ui-comparison","seed"),("./.ui-server","ui-server")):
        result=subprocess.run(["go","build","-o",str(output/binary),package],cwd=new_copy,env=env,capture_output=True,text=True)
        (output/(binary+"-build.log")).write_text(result.stdout+result.stderr)
        result.check_returncode()
    ui_port=free_port()
    ui_base=f"http://127.0.0.1:{ui_port}"
    env["VITE_AFS_CLIENT_MODE"]="http"
    with contextlib.ExitStack() as stack:
        address=stack.enter_context(compare.isolated_redis(args.redis_server,output/"redis"))
        seed=json.loads(compare.command([str(output/"seed"),"-addr",address],env=env))
        ready=output/"api-ready.json"
        api=stack.enter_context(process([str(output/"ui-server"),"-addr",address,"-origin",ui_base,"-ready",str(ready),"-lifetime",str(args.duration)+"s"],output/"api.log",env=env))
        api_base=wait_for_api(api,ready)
        env["VITE_AFS_API_BASE_URL"]=api_base
        vite=stack.enter_context(process(["node",str(modules/"vite/bin/vite.js"),"--config","history-parity.config.mjs","--configLoader","native","--port",str(ui_port)],output/"vite.log",cwd=ui,env=env))
        report={"original_revision":revision,"new_source_sha256":new_source_hash,"new_production_sha256":new_production_hash,"original_drawer_sha256":hashlib.sha256((ui/"src/routes/workspace-studio/-file-history-drawer.tsx").read_bytes()).hexdigest(),"seed":seed,"redis_address":address,"api_url":api_base,"live_drawer_url":ui_base+"/history-parity.html","deleted_drawer_url":ui_base+"/history-parity.html?path=/deleted.txt","method":"Original pinned drawer, hooks, HTTP client, components and CSS unchanged. Test-only HTTP fixture hosts internal/controlplane.NewFileHistoryHandler; no product server command. New UI host adds ReactQuery/theme providers and props only. Original dependencies read via symlink; Vite native config avoids config bundles in original node_modules and all optimization caches stay in disposable UI."}
        (output/"session.json").write_text(json.dumps(report,indent=2)+"\n")
        print(json.dumps(report,indent=2),flush=True)
        if args.component_test:
            completed=subprocess.run(["node",str(modules/"vitest/vitest.mjs"),"run","--config","history-parity-test.config.mjs","--configLoader","native","--reporter=json","--outputFile="+str(output/"component-results.json")],cwd=ui,env=env,capture_output=True,text=True,timeout=120)
            (output/"component.log").write_text(completed.stdout+completed.stderr)
            completed.check_returncode()
            (output/"verification.json").write_text(json.dumps(verify_actions(api_base),indent=2)+"\n")
            print("Original-component jsdom integration passed; all owned processes are stopping.",flush=True)
            return
        deadline=time.monotonic()+args.duration
        while time.monotonic()<deadline:
            if api.poll() is not None or vite.poll() is not None: raise RuntimeError("API or Vite exited; inspect logs")
            time.sleep(.5)


if __name__=="__main__":
    try: main()
    except KeyboardInterrupt: pass

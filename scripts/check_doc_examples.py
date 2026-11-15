#!/usr/bin/env python3
"""Validate complete documentation/example manifests through production Parse + schema.

Builds a temporary pure reader inside the engine module, never runs TestMain,
never executes a manifest command, and removes its own scratch directory.
"""
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[1]
GO = "/opt/homebrew/bin/go" if Path("/opt/homebrew/bin/go").exists() else "go"
READER = r'''package main
import (
 "bytes"
 "fmt"
 "os"
 "strings"
 "github.com/sheathedsharp/option-berth/engine/internal/groups"
 schema "github.com/santhosh-tekuri/jsonschema/v6"
 "gopkg.in/yaml.v3"
)
func main() {
 doc,err:=schema.UnmarshalJSON(bytes.NewReader(groups.MarshalManifestSchema())); must(err)
 compiler:=schema.NewCompiler(); must(compiler.AddResource("https://oberth.invalid/manifest",doc))
 compiled,err:=compiler.Compile("https://oberth.invalid/manifest"); must(err)
 for _,path:=range os.Args[1:] {
  data,err:=os.ReadFile(path); must(err)
  cfg,err:=groups.Parse(path,data); must(err)
  var value any; must(yaml.Unmarshal(data,&value)); must(compiled.Validate(value))
  for _,service:=range cfg.Services {
   expanded:=groups.Expand(service.Cmd,groups.Values{Self:service.Name,Ports:map[string]int{service.Name:43210}})
   if strings.Contains(expanded,"${port}") { panic("unexpanded production port placeholder") }
  }
 }
 fmt.Printf("Production manifest parser + generated schema: %d complete examples; no services executed\n",len(os.Args)-1)
}
func must(err error) { if err!=nil { panic(err) } }
'''


def main():
    cache=json.loads(subprocess.check_output([GO,"env","-json","GOMODCACHE","GOCACHE"],text=True))
    with tempfile.TemporaryDirectory(prefix=".docs-example-",dir=ROOT/"engine") as tmp:
        scratch=Path(tmp); source=scratch/"main.go"; source.write_text(READER)
        manifests=[]
        for name in ("quickstart.md","quickstart.zh.md"):
            blocks=re.findall(r"^```yaml\n(.*?)^```",(ROOT/"docs"/name).read_text(),re.M|re.S)
            if len(blocks)!=1: raise ValueError("Expected one complete quick-start manifest: "+name)
            path=scratch/(name+".yaml"); path.write_text(blocks[0]); manifests.append(path)
        manifests.extend(sorted((ROOT/"examples").rglob("oberth.yaml")))
        env={"PATH":os.environ.get("PATH","/usr/bin:/bin"),"HOME":str(scratch),"GOPROXY":"off","GOWORK":"off","GOTOOLCHAIN":"local","CGO_ENABLED":"0","BERTH_HOME":str(scratch/"state"),"BERTH_RUNTIME_SERVICE_NO_AUTOSTART":"1","BERTH_NO_UPDATE_CHECK":"1","BERTH_STRICT_MANIFEST":"1",**cache}
        subprocess.run([GO,"run","-mod=readonly",str(source),*[str(p) for p in manifests]],cwd=ROOT/"engine",env=env,check=True)
    return 0


if __name__=="__main__":
    raise SystemExit(main())

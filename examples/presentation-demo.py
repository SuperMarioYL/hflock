"""Serve the shipped HF fixture only on loopback and hash its nine files."""
from functools import partial
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
import json, pathlib, subprocess, tempfile, threading
class Quiet(SimpleHTTPRequestHandler):
 def log_message(self,*args): pass
server=ThreadingHTTPServer(('127.0.0.1',0),partial(Quiet,directory='examples/fixture'))
threading.Thread(target=server.serve_forever,daemon=True).start()
try:
 with tempfile.TemporaryDirectory(prefix='hflock-demo-') as work:
  out=str(pathlib.Path(work)/'manifest.json')
  p=subprocess.run(['go','run','./cmd/hflock','verify','examples/weights.lock.yaml','--hf-base',f'http://127.0.0.1:{server.server_port}','--workdir',work,'--manifest',out],text=True,capture_output=True,check=True)
  m=json.loads(pathlib.Path(out).read_text())
  print('source: local fixture; uploaded files: 0')
  for e in m['entries']: print(e['repo'],e['file'],e['size'],e['sha256'])
  print('hashed files:',len(m['entries']))
finally:
 server.shutdown();server.server_close()

#!/usr/bin/env python3
"""Project-scoped Coding tools. Agentic Kit owns model decisions and scheduling."""
import concurrent.futures, hashlib, hmac, http.server, json, os, pathlib, re, shlex, signal, subprocess, threading, time, urllib.request, urllib.error, urllib.parse
ROOT=pathlib.Path(os.environ.get('WORKSPACE_ROOT','/workspace'))
ROOT.mkdir(parents=True,exist_ok=True)
API=os.environ['CODING_URL'].rstrip('/')
TOKEN=os.environ['SERVICE_TOKEN']
PROJECT=int(os.environ['GITLAB_PROJECT_ID'])
GITLAB=os.environ['GITLAB_URL'].rstrip('/')
OPENER=urllib.request.build_opener(urllib.request.ProxyHandler({}))
LOCK=threading.Lock();JOBS={}
def request(method,url,data=None,headers=None,timeout=15):
 req=urllib.request.Request(url,data=json.dumps(data).encode() if data is not None else None,method=method,headers={'Content-Type':'application/json',**(headers or {})})
 with OPENER.open(req,timeout=timeout) as r:return json.load(r)
def api(path,data):return request('POST',API+path,data,{'Authorization':'Bearer '+TOKEN})
def gl(method,path,data=None):return request(method,GITLAB+'/api/v4'+path,data,{'PRIVATE-TOKEN':os.environ['GITLAB_TOKEN']})
def save(path,data):
 tmp=path.with_suffix('.tmp');tmp.write_text(json.dumps(data,ensure_ascii=False));tmp.replace(path)
def scoped(path,scopes):
 import fnmatch
 p=pathlib.PurePosixPath(path)
 return bool(path) and not p.is_absolute() and not any(x in ('..','.git') for x in p.parts) and '\\' not in path and any(path==s.rstrip('/') or path.startswith(s.rstrip('/')+'/') or fnmatch.fnmatchcase(path,s) for s in scopes)
class Job:
 def __init__(self,id,runtime,epoch):
  self.id=id;self.runtime=runtime;self.epoch=epoch;self.lock=threading.Lock();self.operation_lock=threading.Lock();self.stop=threading.Event();self.done=threading.Event();self.proc=None
  self.job=api('/internal/v1/rounds/'+id+'/start',{'worker':runtime,'runtimeEpoch':epoch})
  if PROJECT<=0 or self.job['gitlabProjectId']!=PROJECT:raise ValueError('Tool deployment project scope mismatch')
  self.path=ROOT/id;self.path.mkdir(exist_ok=True);self.repo=self.path/'repo';self.state=self.path/'state.json'
  self.data=json.loads(self.state.read_text()) if self.state.exists() else {'attempts':0}
  self.monitor=threading.Thread(target=self.watch,daemon=True);self.monitor.start()
 def body(self,**extra):return {'runId':self.id,'epoch':self.job['epoch'],'worker':self.runtime,'runtimeEpoch':self.epoch,**extra}
 def gate(self,head=''):
  if self.stop.is_set():raise RuntimeError('Execution stopped')
  return api('/internal/v1/tasks/'+self.job['task']['id']+'/gate',self.body(head=head))
 def watch(self):
  while not self.done.wait(1):
   revoked=False
   try:
    api('/internal/v1/tasks/'+self.job['task']['id']+'/gate',self.body())
    continue
   except urllib.error.HTTPError as e:
    try:code=json.load(e).get('error',{}).get('code','')
    except Exception:code=''
    revoked=code in ('EXECUTION_REVOKED','STALE_EXECUTION')
   except Exception:pass
   self.stop.set()
   with self.lock:
    if self.proc and self.proc.poll() is None:
     os.killpg(self.proc.pid,signal.SIGKILL)
     self.proc.wait()
   # Lease loss stops this attempt, not the business round. A later valid
   # worker can continue from saved steps after the old operation has ended.
   if revoked:
    try:api('/internal/v1/tasks/'+self.job['task']['id']+'/finish',self.body(status='Stopped',output={'reason':'business execution gate closed'}))
    except Exception:continue
    return
 def command(self,args,git=False,check=False):
  self.gate()
  env={k:os.environ[k] for k in ('PATH','LANG','TZ') if k in os.environ}
  env['PYTHONDONTWRITEBYTECODE']='1';env['HOME']=str(self.path);env['GIT_CONFIG_NOSYSTEM']='1';env['GIT_TERMINAL_PROMPT']='0'
  if git and any(x in ('clone','fetch','push','ls-remote') for x in args[:5]):env.update(GIT_ASKPASS='/app/askpass.py',GITLAB_TOKEN=os.environ['GITLAB_TOKEN'])
  with self.lock:
   if self.stop.is_set():raise RuntimeError('Execution stopped')
   self.proc=subprocess.Popen(args,cwd=self.repo if self.repo.exists() else self.path,env=env,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,text=True,start_new_session=True)
   proc=self.proc
  try:out,_=proc.communicate(timeout=60)
  except subprocess.TimeoutExpired:
   os.killpg(proc.pid,signal.SIGKILL);out,_=proc.communicate();raise RuntimeError('Command exceeded 60 seconds')
  finally:
   with self.lock:self.proc=None
  if self.stop.is_set():raise RuntimeError('Execution stopped')
  if proc.returncode and not check:raise RuntimeError(out[-8000:])
  return {'command':shlex.join(args),'exitCode':proc.returncode,'passed':proc.returncode==0,'log':out[-8000:]} if check else out.strip()
 def git(self,*args):return self.command(['git','-c','credential.helper=',*args],git=True)
 def checks(self):return [self.command(shlex.split(cmd),check=True) for cmd in self.job['config']['checks']]
 def invoke(self,name,args):
  t=self.job['task'];cfg=self.job['config'];self.gate()
  if name=='prepare':
   if not self.repo.exists():self.command(['git','clone','--no-hardlinks',GITLAB+'/'+self.job['projectPath']+'.git',str(self.repo)],git=True)
   if not self.data.get('prepared'):
    self.git('fetch','origin',t['head']);self.git('checkout','-B',t['branch'],t['head']);self.data['prepared']=True
    current=self.git('ls-remote','origin','refs/heads/'+t['branch']);self.data['remoteHead']=current.split()[0] if current else ''
    if self.data['remoteHead'] and self.data['remoteHead']!=t['head']:raise ValueError('Remote human commit has priority')
   files=[p for p in self.git('ls-files').splitlines() if scoped(p,cfg['allowedPaths']) and not (self.repo/p).is_symlink()]
   if not files or len(files)>12 or sum((self.repo/p).stat().st_size for p in files)>98304:raise ValueError('Narrow allowedPaths to 1–12 source/test files under 96 KiB')
   rules={}
   for file in files:
    for parent in pathlib.PurePosixPath(file).parents:
     rel=(parent/'AGENTS.md').as_posix();p=self.repo/rel
     if p.is_file() and not p.is_symlink():rules[rel]=p.read_text()
   self.data['baseline']=self.checks();save(self.state,self.data)
   return {'files':{p:(self.repo/p).read_text() for p in files},'rules':rules,'baseline':self.data['baseline'],'repairLimit':cfg['repairLimit']}
  if not self.data.get('prepared'):raise ValueError('Call prepare first')
  if name=='edit_and_check':
   if self.data['attempts']>=cfg['repairLimit']:raise ValueError('Repair limit reached; deliver existing changes')
   edits=args.get('edits',[])
   if not 1<=len(edits)<=12:raise ValueError('Expected 1–12 edits')
   updates={}
   for edit in edits:
    path=edit['path'];p=self.repo/path
    if not scoped(path,cfg['allowedPaths']) or p.is_symlink() or not p.resolve().is_relative_to(self.repo.resolve()):raise ValueError('Edit outside scope')
    old=updates.get(path,p.read_text() if p.exists() else '');before=edit['before']
    if before:
     if old.count(before)!=1:raise ValueError('before must match exactly once')
     new=old.replace(before,edit['after'],1)
    else:
     if p.exists() or path in updates:raise ValueError('Empty before only allowed for new files')
     new=edit['after']
    if p.exists() and (p.name.endswith('_test.go') or p.name.startswith('test_') or '.test.' in p.name or '.spec.' in p.name):
     prior=[x for x in p.read_text().splitlines() if x.strip()];at=0
     for line in new.splitlines():
      if at<len(prior) and line==prior[at]:at+=1
     if at!=len(prior):raise ValueError('Preserve existing tests')
    updates[path]=new
   self.gate()
   for path,content in updates.items():p=self.repo/path;p.parent.mkdir(parents=True,exist_ok=True);p.write_text(content)
   self.data['touched']=sorted(set(self.data.get('touched',[]))|set(updates));self.data['attempts']+=1;self.data['checks']=self.checks();self.data['checkedDiff']=self.git('diff','--binary')
   save(self.state,self.data);return {'checks':self.data['checks'],'attempt':self.data['attempts'],'diff':self.data['checkedDiff'][:24000]}
  if name=='deliver':
   if not self.data.get('checks'):raise ValueError('Run edit_and_check before delivery')
   if args.get('judgment',{}).get('status','NeedsHuman') not in ('Met','Unmet','NeedsHuman'):raise ValueError('Invalid advisory judgment')
   self.data['checks']=self.checks()
   changed=self.git('status','--porcelain')
   if not changed:raise ValueError('No code changes to deliver')
   changed_names=self.git('ls-files','--modified').splitlines()
   touched=self.data.get('touched',[])
   if not touched or any(p not in touched for p in changed_names):raise ValueError('Checks changed files outside model edits')
   self.git('add','--',*touched)
   patch=self.git('diff','--cached')
   if not patch:raise ValueError('No staged source changes')
   self.git('-c','user.name=Forge Coding Agent','-c','user.email=agent@forge.local','commit','-m',str(args['summary'])[:200])
   head=self.git('rev-parse','HEAD')
   self.data['checks']=self.checks()
   if self.git('diff') or self.git('diff','--cached'):raise ValueError('Checks modified the committed revision')
   self.gate(head)
   # Atomic compare-and-swap prevents overwriting a human's intervening commit.
   self.git('push','--force-with-lease=refs/heads/'+t['branch']+':'+self.data['remoteHead'],'origin','HEAD:refs/heads/'+t['branch'])
   self.gate()
   iid=t['mrIid']
   if not iid:
    mr=gl('POST','/projects/'+str(PROJECT)+'/merge_requests',{'source_branch':t['branch'],'target_branch':t['targetBranch'],'title':'Draft: '+t['title'],'description':str(args['summary'])+'\n\nForge task: '+t['id']});iid=mr['iid']
   result={'status':'Passed' if all(x['passed'] for x in self.data['checks']) else 'Failed','head':head,'mrIid':iid,'output':{'patch':patch,'checksCommit':head,'configVersion':cfg['version'],'checks':[{'attempt':self.data['attempts'],'passed':all(c['passed'] for c in self.data['checks']),'commands':self.data['checks'],'log':'\n'.join(c['command']+'\n'+c['log'] for c in self.data['checks'])}],'baseline':self.data.get('baseline',[]),'summary':args['summary'],'judgment':args.get('judgment',{'status':'NeedsHuman','summary':'需要人工核对验收条件'})}}
   self.gate(head);api('/internal/v1/tasks/'+t['id']+'/finish',self.body(**result));self.done.set();save(self.state,{**self.data,'delivery':result});return result
  raise ValueError('Unknown tool')
class Handler(http.server.BaseHTTPRequestHandler):
 def log_message(self,*args):pass
 def reply(self,code,value):
  raw=json.dumps(value,ensure_ascii=False).encode();self.send_response(code);self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(raw)));self.end_headers();self.wfile.write(raw)
 def do_GET(self):self.reply(200 if self.path=='/healthz' else 404,{'status':'ok'})
 def do_POST(self):
  if not hmac.compare_digest(self.headers.get('Authorization',''),'Bearer '+os.environ['TOOLS_TOKEN']):self.reply(401,{'error':'unauthorized'});return
  match=re.fullmatch(r'/rounds/(run_[a-f0-9]+)/([a-z_]+)',self.path)
  if not match:self.reply(404,{'error':'not found'});return
  try:
   length=int(self.headers.get('Content-Length','0'))
   if length<1 or length>262144:raise ValueError('Invalid body size')
   args=json.loads(self.rfile.read(length));round,name=match.groups();runtime=self.headers.get('X-Agentic-Run-ID','');epoch=int(self.headers.get('X-Agentic-Epoch','0'));operation=self.headers.get('Idempotency-Key','')
   if not runtime or epoch<1 or not operation.startswith(runtime+':'):raise ValueError('Missing execution identity')
   with LOCK:
    job=JOBS.get(round)
    if job and job.runtime!=runtime:raise ValueError('Execution identity changed')
    if job and job.epoch!=epoch:
     if epoch<=job.epoch:raise ValueError('Stale execution attempt')
     # Do not let a replacement overlap an unfinished external operation.
     with job.operation_lock:
      job.done.set()
      job.monitor.join(timeout=20)
      if job.monitor.is_alive():raise ValueError('Previous execution stop is not confirmed')
      job=Job(round,runtime,epoch);JOBS[round]=job
    if not job:job=Job(round,runtime,epoch);JOBS[round]=job
   # Serialize tools for a round. An uncertain operation is never automatically repeated.
   key=hashlib.sha256(operation.encode()).hexdigest();record=job.path/(key+'.json');digest=hashlib.sha256(json.dumps([name,args],sort_keys=True).encode()).hexdigest()
   with LOCK:
    if record.exists():
     old=json.loads(record.read_text())
     if old['digest']!=digest or 'result' not in old:raise ValueError('Operation needs confirmation')
     self.reply(200,old['result']);return
    save(record,{'digest':digest})
   with job.operation_lock:
    result=job.invoke(name,args);save(record,{'digest':digest,'result':result})
   self.reply(200,result)
  except (ValueError,RuntimeError,KeyError) as e:self.reply(409,{'error':str(e)[:1000]})
  except Exception:self.reply(503,{'error':'Tool dependency failed; inspect execution before retrying'})
if __name__=='__main__':
 if PROJECT<=0:raise SystemExit('GITLAB_PROJECT_ID must be a positive, explicitly approved project')
 http.server.ThreadingHTTPServer(('0.0.0.0',8080),Handler).serve_forever()

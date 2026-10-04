import importlib.util,os,pathlib,tempfile,threading,unittest
from unittest.mock import patch
spec=importlib.util.spec_from_file_location('coding_tools',pathlib.Path(__file__).with_name('server.py'));tools=importlib.util.module_from_spec(spec)
with tempfile.TemporaryDirectory() as root:
 with patch.dict(os.environ,{'WORKSPACE_ROOT':root,'GITLAB_PROJECT_ID':'77','CODING_URL':'http://unused','GITLAB_URL':'http://unused','SERVICE_TOKEN':'server-secret'}):spec.loader.exec_module(tools)
class ToolTests(unittest.TestCase):
 def job(self,root):
  job=tools.Job.__new__(tools.Job);job.path=pathlib.Path(root);job.repo=job.path;job.lock=threading.Lock();job.stop=threading.Event();job.proc=None;job.gate=lambda *args:None;return job
 def test_scope_rejects_escape(self):
  for path,allowed in [('internal/a.go',True),('internal/nested/a.go',True),('../a.go',False),('/internal/a.go',False),('internal/../secret',False),('.git/config',False),('internal-other/a.go',False),('internal\\a.go',False)]:
   with self.subTest(path=path):self.assertEqual(tools.scoped(path,['internal/']),allowed)
 def test_checks_do_not_inherit_credentials_or_invoke_shell(self):
  with tempfile.TemporaryDirectory() as root:
   job=self.job(root)
   with patch.dict(os.environ,{'SERVICE_TOKEN':'hidden','GITLAB_TOKEN':'hidden','RUNTIME_TOKEN':'hidden'}):
    out=job.command(['python3','-c','import os;print(any(k in os.environ for k in ("SERVICE_TOKEN","GITLAB_TOKEN","RUNTIME_TOKEN")))'])
   self.assertEqual(out,'False');self.assertEqual(job.command(['echo','ok;','touch','injected']),'ok; touch injected');self.assertFalse((job.path/'injected').exists())
 def test_revocation_prevents_starting_another_process(self):
  with tempfile.TemporaryDirectory() as root:
   job=self.job(root);job.stop.set()
   with self.assertRaisesRegex(RuntimeError,'stopped'):job.command(['touch','should-not-exist'])
   self.assertFalse((job.path/'should-not-exist').exists())
if __name__=='__main__':unittest.main()

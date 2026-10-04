#!/usr/bin/env python3
import os,sys
print('oauth2' if 'username' in sys.argv[1].lower() else os.environ['GITLAB_TOKEN'])

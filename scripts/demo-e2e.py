#!/usr/bin/env python3
"""Two cookie clients against the real etcd + Mooncake Compose stack."""
import http.cookiejar
import json
import os
import queue
import subprocess
import threading
import time
import urllib.error
import urllib.request

BASE = os.environ.get('GATEWAY_URL', 'http://localhost:18080')

class Client:
    def __init__(self):
        self.cookies = http.cookiejar.CookieJar()
        self.http = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(self.cookies))
    def api(self, path, method='GET', data=None, expected=200):
        request = urllib.request.Request(BASE+path, method=method,
            data=json.dumps(data).encode() if data is not None else (b'' if method=='POST' else None),
            headers={'Content-Type':'application/json'})
        try:
            response=self.http.open(request, timeout=8)
        except urllib.error.HTTPError as e:
            response=e
        with response:
            body=json.load(response)
            assert response.status==expected, (path,response.status,body)
            return body
    def cookie(self):
        return '; '.join(c.name+'='+c.value for c in self.cookies)

class Stream:
    def __init__(self, client, last_id=''):
        self.queue=queue.Queue()
        self.seen=[]
        self.last_id=last_id
        self.closed=False
        self.response=urllib.request.urlopen(urllib.request.Request(BASE+'/api/events',headers={
            'Cookie':client.cookie(),'Last-Event-ID':last_id}),timeout=15)
        self.thread=threading.Thread(target=self.read,daemon=True)
        self.thread.start()
    def read(self):
        event={}
        try:
            for raw in self.response:
                line=raw.decode().strip()
                if not line:
                    if 'data' in event:
                        event['data']=json.loads(event['data'])
                        self.last_id=event.get('id',self.last_id)
                        self.seen.append(event)
                        self.queue.put(event)
                    event={}
                elif ': ' in line:
                    key,value=line.split(': ',1)
                    event[key]=value
        except (OSError,ValueError,AttributeError):
            if not self.closed:
                self.queue.put({'error':'unexpected stream termination'})
    def until(self, predicate, timeout=20):
        deadline=time.monotonic()+timeout
        while time.monotonic()<deadline:
            try: event=self.queue.get(timeout=max(.01,deadline-time.monotonic()))
            except queue.Empty: break
            assert 'error' not in event,event
            if predicate(event): return event
        raise AssertionError('SSE event timeout: '+repr(self.seen[-3:]))
    def state(self, seq=None, owner=None, epoch=None, finished=False):
        return self.until(lambda e:e.get('event')=='game_state' and
            (seq is None or e['data']['control']['committedSeq']==seq) and
            (owner is None or e['data']['control']['ownerId']==owner) and
            (epoch is None or e['data']['control']['epoch']>=epoch) and
            (not finished or e['data']['game']['status']!='active'))
    def close(self):
        self.closed=True
        self.response.close()

def wait(fn, timeout=20):
    deadline=time.monotonic()+timeout
    while time.monotonic()<deadline:
        result=fn()
        if result:return result
        time.sleep(.2)
    raise AssertionError('condition timeout')

def main():
    a,b,outsider=Client(),Client(),Client()
    original=a.api('/api/me')
    assert a.api('/api/me')['client_id']==original['client_id']
    assert a.api('/api/me','PATCH',{'nickname':'White moon'})['nickname']=='White moon'
    assert a.api('/api/me','PATCH',{'nickname':''},400)['error']
    reset=a.api('/api/me/reset','POST')
    assert reset['client_id']!=original['client_id']
    b.api('/api/me');outsider.api('/api/me')
    a.api('/api/matchmaking/enqueue','POST')
    assert a.api('/api/matchmaking/status')['status']=='searching'
    a.api('/api/matchmaking/enqueue','DELETE')
    assert a.api('/api/matchmaking/status')['status']=='home'
    streams=[];killed=None
    try:
        sa,sb=Stream(a),Stream(b);streams += [sa,sb]
        a.api('/api/matchmaking/enqueue','POST');b.api('/api/matchmaking/enqueue','POST')
        match=wait(lambda:a.api('/api/me').get('game_id'))
        assert wait(lambda:b.api('/api/me').get('game_id'))==match
        state=wait(lambda:(s if (s:=a.api('/api/games/current'))['control']['ownerId'] else None))
        control=state['control']
        white,black=(a,b) if control['white_client_id']==a.api('/api/me')['client_id'] else (b,a)
        live=sa if white is a else sb
        offline=sb if white is a else sa
        live.state(seq=0);offline.state(seq=0)
        outsider.api(f'/api/games/{match}/moves','POST',{'move':'f2f3','client_id':control['white_client_id']},403)
        outsider.api(f'/api/games/{match}',expected=403)
        black.api(f'/api/games/{match}/moves','POST',{'move':'f2f3'},403)
        white.api(f'/api/games/{match}/moves','POST',{'move':'f2f3'})
        live.state(seq=1);offline.state(seq=1)
        last_id=offline.last_id
        offline.close()
        white.api(f'/api/games/{match}/moves','POST',{'move':'e7e5'},403)
        black.api(f'/api/games/{match}/moves','POST',{'move':'e7e5'})
        live.state(seq=2)
        before=white.api('/api/games/current')['control']
        target='worker-b' if before['ownerId']=='worker-a' else 'worker-a'
        white.api(f'/api/games/{match}/migrate','POST',{'target':target})
        live.state(seq=2,owner=target,epoch=before['epoch']+1)
        killed=target
        subprocess.run(['docker','compose','kill','-s','SIGKILL',killed],check=True)
        takeover=live.until(lambda e:e.get('event')=='game_state' and
            bool(e['data']['control']['ownerId']) and e['data']['control']['ownerId']!=killed and
            e['data']['control']['epoch']>=before['epoch']+2)
        assert takeover['data']['control']['committedSeq']==2
        white.api(f'/api/games/{match}/moves','POST',{'move':'g2g4'})
        black.api(f'/api/games/{match}/moves','POST',{'move':'d8h4'})
        live.state(seq=4,finished=True)
        reconnect=Stream(black,last_id);streams.append(reconnect)
        reconnect.state(seq=4,finished=True)
        recovered=[e['data']['seq'] for e in reconnect.seen if e.get('event')=='move_committed']
        assert recovered==[2,3,4],recovered
        kinds=[e['data'].get('kind') for e in reconnect.seen if e.get('event')=='ownership']
        assert 'migrating' in kinds and 'released' in kinds and 'acquired' in kinds,kinds
        assert [e['data']['seq'] for e in live.seen if e.get('event')=='move_committed']==[1,2,3,4]
        assert white.api('/api/games/current')['game']['status']=='0-1'
        white.api(f'/api/games/{match}/moves','POST',{'move':'a2a3'},422)
        white.api('/api/games/current/play-again','POST');black.api('/api/games/current/play-again','POST')
        next_game=wait(lambda:(g if (g:=white.api('/api/me').get('game_id')) and g!=match else None))
        assert wait(lambda:black.api('/api/me').get('game_id'))==next_game
        print('PASS: identity/reset/nickname, cancel, two-client match, authorization, mate, play-again')
        print('PASS: live SSE survives migrate + SIGKILL; reconnect recovers moves 2/3/4 and ownership events')
    finally:
        for s in streams:s.close()
        if killed:subprocess.run(['docker','compose','up','-d',killed],check=True)

if __name__=='__main__':main()

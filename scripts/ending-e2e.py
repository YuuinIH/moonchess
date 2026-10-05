#!/usr/bin/env python3
"""Real Compose checks for persisted clocks and non-move game endings."""
import os
from pathlib import Path
import runpy
import subprocess
import urllib.error
import urllib.request

helpers = runpy.run_path(str(Path(__file__).with_name('demo-e2e.py')))
Client, Stream, wait = (helpers[name] for name in ('Client', 'Stream', 'wait'))


def ready():
    try:
        with urllib.request.urlopen(helpers['BASE'] + '/healthz', timeout=2) as response:
            return response.status == 200
    except (OSError, urllib.error.URLError):
        return False


def pair(a, b):
    old = a.api('/api/me').get('game_id')
    b.api('/api/me')
    a.api('/api/games/current/play-again', 'POST')
    b.api('/api/games/current/play-again', 'POST')
    game = wait(lambda: (g if (g := a.api('/api/me').get('game_id')) and g != old else None))
    assert wait(lambda: b.api('/api/me').get('game_id')) == game
    snapshot = wait(lambda: (s if (s := a.api('/api/games/current'))['control']['ownerId'] else None))
    white, black = (a, b) if snapshot['control']['white_client_id'] == a.api('/api/me')['client_id'] else (b, a)
    return game, snapshot, white, black


def commands():
    a, b, outsider = Client(), Client(), Client()
    outsider.api('/api/me')
    streams, killed = [], None
    try:
        game, snapshot, white, black = pair(a, b)
        sa, sb = Stream(white), Stream(black)
        streams.extend([sa, sb])
        sa.state(seq=0); sb.state(seq=0)
        before = snapshot['game']['clock']
        white.api(f'/api/games/{game}/moves', 'POST', {'move': 'e2e4'})
        sa.state(seq=1); sb.state(seq=1)
        moving = white.api('/api/games/current')
        assert moving['game']['clock']['blackMs'] == before['blackMs']
        assert 0 < moving['game']['clock']['whiteMs'] <= before['whiteMs'] + before['incrementMs']
        last_id = sb.last_id
        sb.close()
        outsider.api(f'/api/games/{game}/resign', 'POST', expected=403)
        outsider.api(f'/api/games/{game}/abandon', 'POST', expected=403)
        owner = moving['control']['ownerId']
        target = 'worker-b' if owner == 'worker-a' else 'worker-a'
        white.api(f'/api/games/{game}/migrate', 'POST', {'target': target})
        transferred = sa.state(seq=1, owner=target, epoch=moving['control']['epoch'] + 1)
        assert transferred['data']['game']['clock'] == moving['game']['clock']
        killed = target
        subprocess.run(['docker', 'compose', 'kill', '-s', 'SIGKILL', killed], check=True)
        takeover = sa.until(lambda e: e.get('event') == 'game_state' and
                            bool(e['data']['control']['ownerId']) and e['data']['control']['ownerId'] != killed)
        assert takeover['data']['game']['clock'] == moving['game']['clock']
        # White resigns on black's turn, through the new owner.
        result = white.api(f'/api/games/{game}/resign', 'POST')
        assert result['game']['status'] == '0-1' and result['game']['reason'] == 'resignation'
        assert result['control']['committedSeq'] == 2 and result['game']['moves'] == ['e2e4']
        sa.state(seq=2, finished=True)
        reconnect = Stream(black, last_id); streams.append(reconnect)
        event = reconnect.until(lambda e: e.get('event') == 'game_finished')
        assert event['data']['game']['reason'] == 'resignation'
        assert not [e for e in reconnect.seen if e.get('event') == 'move_committed']
        again = white.api(f'/api/games/{game}/resign', 'POST')
        assert again['control']['committedSeq'] == 2
        black.api(f'/api/games/{game}/moves', 'POST', {'move': 'e7e5'}, expected=422)
        for s in streams: s.close()
        streams = []
        subprocess.run(['docker', 'compose', 'up', '-d', killed], check=True)
        killed = None
        game, snapshot, white, black = pair(a, b)
        sa, sb = Stream(white), Stream(black); streams.extend([sa, sb])
        sa.state(seq=0); sb.state(seq=0)
        result = black.api(f'/api/games/{game}/abandon', 'POST')
        assert result['game']['status'] == '1-0' and result['game']['reason'] == 'abandonment'
        assert result['control']['committedSeq'] == 1 and not result['game']['moves']
        sa.state(seq=1, finished=True); sb.state(seq=1, finished=True)
        # Finished players can reset their anonymous identity without stranding anyone.
        old = black.api('/api/me')['client_id']
        assert black.api('/api/me/reset', 'POST')['client_id'] != old
        print('PASS: clocks persist through migration/SIGKILL, off-turn resignation, authorized abandonment, SSE finish reconnect, retry, reset')
    finally:
        for s in streams: s.close()
        if killed: subprocess.run(['docker', 'compose', 'up', '-d', killed], check=True)


def timeout():
    # Short clocks are minted only for new test games. Restore normal gateway
    # configuration even on failure; existing game clocks are never rewritten.
    test_env = dict(os.environ, GAME_TIME='12s', GAME_INCREMENT='0s')
    subprocess.run(['docker', 'compose', 'up', '-d', 'gateway'], env=test_env, check=True)
    streams, killed = [], None
    try:
        wait(ready)
        a, b = Client(), Client()
        game, snapshot, white, black = pair(a, b)
        assert snapshot['game']['clock']['whiteMs'] == 12000
        sa, sb = Stream(white), Stream(black); streams.extend([sa, sb])
        sa.state(seq=0); sb.state(seq=0)
        white.api(f'/api/games/{game}/moves', 'POST', {'move': 'e2e4'})
        sa.state(seq=1); sb.state(seq=1)
        moving = white.api('/api/games/current')
        anchor = moving['game']['clock']
        killed = moving['control']['ownerId']
        subprocess.run(['docker', 'compose', 'kill', '-s', 'SIGKILL', killed], check=True)
        takeover = sa.until(lambda e: e.get('event') == 'game_state' and
                            bool(e['data']['control']['ownerId']) and e['data']['control']['ownerId'] != killed)
        assert takeover['data']['game']['clock'] == anchor
        sb.close()  # No commands and no connection from the player whose time expires.
        result = sa.until(lambda e: e.get('event') == 'game_finished', timeout=25)['data']
        assert result['game']['status'] == '1-0' and result['game']['reason'] == 'timeout'
        assert result['game']['clock']['blackMs'] == 0 and result['game']['clock']['turnStartedUnixMs'] == 0
        assert result['control']['committedSeq'] == 2 and result['game']['moves'] == ['e2e4']
        black.api(f'/api/games/{game}/moves', 'POST', {'move': 'e7e5'}, expected=422)
        reconnect = Stream(black, f'{game}:1:1:0'); streams.append(reconnect)
        event = reconnect.until(lambda e: e.get('event') == 'game_finished')
        assert event['data']['game']['reason'] == 'timeout'
        print('PASS: disconnected player times out after owner SIGKILL; no clock reset, no fake chess move, SSE replay, late move rejected')
    finally:
        for s in streams: s.close()
        if killed: subprocess.run(['docker', 'compose', 'up', '-d', killed], check=True)
        subprocess.run(['docker', 'compose', 'up', '-d', 'gateway'], check=True)


if __name__ == '__main__':
    commands()
    timeout()

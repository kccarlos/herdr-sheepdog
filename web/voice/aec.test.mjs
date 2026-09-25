// Unit tests for the echo-cancellation hint logic: `make test-web`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { aecStatus, aecWarn, isMobile, AEC_HINT } from './aec.js';

const UA = {
  desktopChrome: 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36',
  androidChrome: 'Mozilla/5.0 (Linux; Android 15; Pixel 9) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Mobile Safari/537.36',
  iphoneSafari: 'Mozilla/5.0 (iPhone; CPU iPhone OS 19_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/19.0 Mobile/15E148 Safari/604.1',
  ipadOS: 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/19.0 Safari/605.1.15',
};

test('aecStatus reads getSettings().echoCancellation', () => {
  assert.equal(aecStatus({ echoCancellation: true }), 'true');
  assert.equal(aecStatus({ echoCancellation: false }), 'false');
  assert.equal(aecStatus({}), 'unknown');
  assert.equal(aecStatus(undefined), 'unknown');
  assert.equal(aecStatus({ echoCancellation: 'remote-only' }), 'unknown');
});

test('isMobile: Android, iPhone and iPadOS are mobile; desktop Chrome and a Mac are not', () => {
  assert.equal(isMobile(UA.androidChrome), true);
  assert.equal(isMobile(UA.iphoneSafari), true);
  assert.equal(isMobile(UA.ipadOS, 5), true); // iPadOS Safari looks like a Mac with a touch screen
  assert.equal(isMobile(UA.ipadOS, 0), false);
  assert.equal(isMobile(UA.desktopChrome), false);
  assert.equal(isMobile(''), false);
});

test('aecWarn: off anywhere, or unknown on a mobile browser, shows the hint', () => {
  assert.equal(aecWarn('false', false), true);
  assert.equal(aecWarn('false', true), true);
  assert.equal(aecWarn('unknown', true), true);
  assert.equal(aecWarn('unknown', false), false);
  assert.equal(aecWarn('true', true), false);
  assert.equal(aecWarn('true', false), false);
  assert.match(AEC_HINT, /headphones/);
});

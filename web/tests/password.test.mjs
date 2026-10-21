import { test } from 'node:test'
import assert from 'node:assert/strict'
import { validPassword } from '../src/password.ts'

test('password requires eight characters with letters and digits; symbols are optional', () => {
  for (const value of ['abcd1234', 'ABCD1234', 'Ab12!@34', 'ab12中文测试', 'a'.repeat(71) + '1']) {
    assert.equal(validPassword(value), true)
  }
  for (const value of ['', 'abc1234', 'abcdefgh', '12345678', '!@#$%^&*', 'a1中文测试题', 'a'.repeat(72) + '1', 'abc1234\n', 'abc1234\r', 'abc1234\x00']) {
    assert.equal(validPassword(value), false)
  }
})

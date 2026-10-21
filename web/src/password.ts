export const passwordRequirement = '密码至少 8 位，需同时包含字母和数字，最多 72 字节，不能包含换行或空字符'

export function validPassword(password: string): boolean {
  return Array.from(password).length >= 8 && new TextEncoder().encode(password).length <= 72 &&
    /[a-z]/i.test(password) && /[0-9]/.test(password) && !/[\x00\r\n]/.test(password)
}

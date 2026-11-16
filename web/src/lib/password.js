// 口令表单的共用校验。
//
// 登录门（首次设置）与设置页（修改口令）用的是同一套规则，
// 分散在两个组件里会让文案与长度下限慢慢分叉。

/** 口令长度下限，与后端 validatePassword 保持一致。 */
export const MIN_PASSWORD_LEN = 8;

/**
 * 校验口令输入。
 *
 * 返回错误文案；通过时返回 null。
 */
export function checkPassword(password, confirm) {
  if ((password ?? '').length < MIN_PASSWORD_LEN) {
    return `口令至少需要 ${MIN_PASSWORD_LEN} 个字符`;
  }
  if (confirm !== undefined && password !== confirm) {
    return '两次输入的口令不一致';
  }
  return null;
}

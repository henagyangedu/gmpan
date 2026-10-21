package app

import "fmt"

// Only fixed diagnostic text and numeric codes may reach the administrator.
// The provider's msg/data may contain credentials or verification codes.
type smsRequestError struct {
	public       apiError
	httpStatus   int
	platformCode *int
	timeout      bool
}

func (e *smsRequestError) Error() string { return e.public.Error() }
func (e *smsRequestError) Unwrap() error { return e.public }

func (e *smsRequestError) diagnostic() string {
	if e.httpStatus == 0 {
		if e.timeout {
			return "请求短信平台超时，受理结果尚未确认。请先检查手机及平台发送记录，不要连续重发。"
		}
		return "无法连接短信平台，请检查部署服务器的出站网络、DNS、HTTPS 证书及接口基址。受理结果尚未确认，不要连续重发。"
	}
	if e.platformCode != nil && *e.platformCode != 0 {
		return fmt.Sprintf("短信平台拒绝受理（code=%d，HTTP %d）：%s", *e.platformCode, e.httpStatus, smsCodeAdvice(*e.platformCode))
	}
	return fmt.Sprintf("短信平台响应异常（HTTP %d），请确认接口基址包含 /api/v1，且返回 JSON 中含数值型 code=0；检查服务器代理或网关。", e.httpStatus)
}

func smsCodeAdvice(code int) string {
	switch code {
	case 10001, 10002:
		return "请求参数错误或缺失，请核对登录验证码场景标识及接口基址。"
	case 20001:
		return "签名校验失败，请核对 AppKey、AppSecret 是否正确且属于同一项目。"
	case 20002:
		return "时间戳超出允许范围，请校准部署服务器时间；平台允许偏差不超过 300 秒。"
	case 20003:
		return "请求随机串被判定重复，持续出现时请联系维护人员；不要连续重发。"
	case 20004:
		return "短信项目已禁用，请联系短信平台启用项目。"
	case 20005:
		return "服务器出口 IP 不在白名单，请在短信平台登记实际出口公网 IP，不能填写服务器内网 IP。"
	case 20006:
		return "平台限频，请等待冷却结束后再试，并核对项目限频设置。"
	case 30001, 30002, 30003, 30004:
		return "额度不足、达到日/月限额或缺少额度账本，请检查项目额度与限额。"
	case 40001, 40002:
		return "短信模板不存在或已停用，请检查登录场景关联的模板。"
	case 40003:
		return "模板未授权给当前项目，请联系平台开通场景及模板授权。"
	case 40004:
		return "模板变量校验失败，请联系平台检查验证码场景与模板变量。"
	case 40005:
		return "短信内容被平台拦截，请联系平台核对验证码模板。"
	case 40006:
		return "平台认为手机号格式不正确，请核对接收号码。"
	case 40007:
		return "接收号码在平台黑名单中，请联系平台核对。"
	case 40008, 40009:
		return "发送渠道不可用或发送失败，请检查平台发送记录并联系平台；不要连续重发。"
	case 50000, 50001:
		return "短信平台系统繁忙，请稍后再试。"
	default:
		return "请在短信平台查询此返回码，核对项目、场景授权、模板、额度和出口 IP 白名单。"
	}
}

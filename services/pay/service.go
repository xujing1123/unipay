// Copyright 2024 unipay Author. All Rights Reserved.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//      http://www.apache.org/licenses/LICENSE-2.0
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package pay

import (
	"errors"
	"fmt"
	"github.com/go-the-way/unipay/deps/pkg"
	"github.com/go-the-way/unipay/events/logevent"
	"github.com/go-the-way/unipay/models"
	"github.com/go-the-way/unipay/services/channel"
	"github.com/go-the-way/unipay/services/channelparam"
	"github.com/go-the-way/unipay/services/order"
	"net/http"
	"strconv"
)

type service struct{}

func (s *service) getParams(ps []models.ChannelParam) [][2]string {
	var params [][2]string
	for _, a := range ps {
		params = append(params, [2]string{a.Name, a.Value})
	}
	return params
}

func (s *service) ReqPay(req Req) (resp Resp, err error) {
	pm, err := channel.Get(channel.GetReq{Id: req.ChannelId})
	if err != nil {
		return
	}

	if req.Subject == "" {
		req.Subject = pm.ProductName
	}

	if err = channelValid(pm, req); err != nil {
		return
	}

	pmm, err := channelparam.GetChannelId(channelparam.GetChannelIdReq{ChannelId: req.ChannelId})
	if err != nil {
		return
	}

	// 订单id
	orderId := pkg.RandStr(20)

	// 汇率计算
	amountYuan, amountFen, getErr := rateHandle(pm.Currency, req.AmountYuan, req.AmountCurrency, pm.KeepDecimal == 1, orderId)
	if getErr != nil {
		err = getErr
		return
	}

	switch pm.Type {
	default:
		err = errors.New("不支持的渠道类型：" + pm.Type)
		logevent.Save(models.NewLogError(orderId, err))
		return

	case models.OrderTypeNormal:
		evalEdParams, evalErr := pkg.EvalParams(req.ToMap(orderId, amountYuan, amountFen), pm.ToMap(), s.getParams(pmm.List))
		if evalErr != nil {
			err = evalErr
			return
		}
		respStr, respMap, respErr := reqDo(pm, pmm, evalEdParams, orderId)
		if respErr != nil {
			err = respErr
			return
		}
		return reqCallback(req, pm, respStr, respMap, orderId)

	case models.OrderTypeErc20, models.OrderTypeTrc20:
		return e20Run(req, pm, orderId)

	}
}

func rateHandle(channelCurrency, orderAmount, amountCurrency string, keepDecimal bool, orderId string) (realAmountYuan, realAmountFen string, err error) {
	// 	0. 查询美元汇率
	usdRate, usdErr := getUsdRate(orderId)
	if usdErr != nil {
		err = usdErr
		return
	}
	// usdRate := float64(7.1201)
	respAmount, _ := strconv.ParseFloat(orderAmount, 32)
	if amountCurrency != channelCurrency {
		orderAmountFloat, _ := strconv.ParseFloat(orderAmount, 32)
		usd2cny := func() { respAmount = orderAmountFloat * usdRate }
		cny2usd := func() { respAmount = orderAmountFloat / usdRate }
		// amountCurrency => channelCurrency
		switch amountCurrency {
		case "USD":
			// channelCurrency: CNY
			// USD => CNY
			usd2cny()
		case "CNY":
			// channelCurrency: USD
			// CNY => USD
			cny2usd()
		}
	}

	if keepDecimal {
		realAmountYuan = fmt.Sprintf("%.2f", respAmount)
		realAmountYuan1, _ := strconv.ParseFloat(realAmountYuan, 32)
		realAmountFen = fmt.Sprintf("%d", int(realAmountYuan1*100))
	} else {
		realAmountYuan = fmt.Sprintf("%d", int(respAmount))
		realAmountFen = fmt.Sprintf("%d", int(respAmount)*100)
	}
	return
}

func (s *service) NotifyPay(req *http.Request, resp http.ResponseWriter, r NotifyReq) (err error) {
	notifyPayReturn := func(resp http.ResponseWriter, c channel.GetResp) {
		ct := ctMap[c.NotifyPayReturnContentType]
		resp.Header().Set("Content-Type", ct)
		_, _ = resp.Write([]byte(c.NotifyPayReturnContent))
		resp.WriteHeader(http.StatusOK)
	}
	c, cErr := channel.Service.Get(channel.GetReq{Id: r.ChannelId})
	if cErr != nil {
		return cErr
	}
	odr, oErr := order.Service.GetIdAndBusinessId(order.GetIdAndBusinessIdReq{Id: r.OrderId, BusinessId1: r.BusinessId1, BusinessId2: r.BusinessId2, BusinessId3: r.BusinessId3})
	if oErr != nil {
		return oErr
	}
	if odr.State == models.OrderStatePaid {
		notifyPayReturn(resp, c)
		return
	}
	respMap := ctRespFuncMap[c.NotifyPayContentType](req)
	paySuccess, pErr := pkg.EvalBool(c.NotifyPaySuccessExpr, respMap)
	if pErr != nil {
		err = errors.New(fmt.Sprintf("回调处理成功，但是解析回调支付成功计算表达式，错误：%s", pErr.Error()))
		models.NewLogError(r.OrderId, err)
		return
	}
	if paySuccess {
		var tradeId string
		if expr := c.NotifyPayIdExpr; expr != "" {
			if tradeId, err = pkg.EvalString(expr, respMap); err != nil {
				return
			}
		}
		if err = order.Paid(order.PaidReq{IdReq: order.IdReq{Id: r.OrderId}, TradeId: tradeId}); err != nil {
			return
		}
		notifyPayReturn(resp, c)
		s.callback(r)
	}
	return
}

func (s *service) callback(req NotifyReq) {
	if fn := req.Callback; fn != nil {
		go func() { resp, _ := order.Get(order.GetReq{Id: req.OrderId}); fn(req, &resp.Order) }()
	}
}

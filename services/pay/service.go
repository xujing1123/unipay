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
	"net/http"

	"github.com/rwscode/unipay/deps/db"
	"github.com/rwscode/unipay/deps/pkg"
	"github.com/rwscode/unipay/deps/script"
	"github.com/rwscode/unipay/models"
	"github.com/rwscode/unipay/services/channel"
	"github.com/rwscode/unipay/services/channelparam"
	"github.com/rwscode/unipay/services/order"
)

type service struct{}

func (s *service) ReqPay(req Req) (resp Resp, err error) {
	pm, err := channel.Get(channel.GetReq{Id: req.ChannelId})
	if err != nil {
		return
	}

	if err = channelValid(pm, req); err != nil {
		return
	}

	pmm, err := channelparam.GetChannelId(channelparam.GetChannelIdReq{ChannelId: req.ChannelId})
	if err != nil {
		return
	}

	// 订单id
	orderId := pkg.RandStr(30)

	evalEdParams, err := pkg.EvalParams(req.ToMap(orderId), pm.ToMap(), pmm.List, orderId)
	if err != nil {
		return
	}

	respMap, err := reqDo(pm, pmm, evalEdParams)

	return reqCallback(req, pm, respMap, orderId)
}

func (s *service) NotifyPay(req *http.Request, resp http.ResponseWriter, r NotifyReq, paidCallback func(req NotifyReq)) (err error) {
	notifyPayReturn := func(resp http.ResponseWriter, c channel.GetResp) {
		ct := ctMap[c.NotifyPayReturnContentType]
		resp.Header().Set("Content-Type", ct)
		_, _ = resp.Write([]byte(c.NotifyPayReturnContent))
		go func() {
			if fn := paidCallback; fn != nil {
				fn(r)
			}
		}()
	}
	c, cErr := channel.Service.Get(channel.GetReq{Id: r.ChannelId})
	if cErr != nil {
		return cErr
	}
	odr, oErr := order.Service.GetIdAndBusinessId(order.GetIdAndBusinessIdReq{Id: r.OrderId, BusinessId1: r.BusinessId1, BusinessId2: r.BusinessId2, BusinessId3: r.BusinessId3})
	if oErr != nil {
		return oErr
	}
	if odr.State == models.OrderStatePaySuccess {
		notifyPayReturn(resp, c)
		return
	}
	respMap := ctRespFuncMap[c.NotifyPayContentType](req)
	paySuccess, pErr := script.EvalBool(c.NotifyPaySuccessExpr, respMap)
	if pErr != nil {
		err = errors.New(fmt.Sprintf("回调处理成功，但是解析回调支付成功计算表达式：%s，错误：%s", c.NotifyPaySuccessExpr, pErr.Error()))
		return
	}
	var tradeId string
	if expr := c.NotifyPayIdExpr; expr != "" {
		if tradeId, err = script.EvalString(expr, respMap); err != nil {
			return
		}
	}
	stMap := map[bool]byte{true: models.OrderStatePaySuccess, false: models.OrderStatePayFailure}
	payTime := ""
	if paySuccess {
		payTime = pkg.TimeNowStr()
	}
	if err = db.GetDb().Model(&models.Order{Id: r.OrderId}).Updates(&models.Order{TradeId: tradeId, State: stMap[paySuccess], PayTime: payTime, UpdateTime: pkg.TimeNowStr()}).Error; err != nil {
		return
	}
	notifyPayReturn(resp, c)
	return
}

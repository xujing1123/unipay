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

package tronscanevent

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/go-the-way/unipay/events/backupevent"
	"io"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/go-the-way/unipay/deps/pkg"
	"github.com/go-the-way/unipay/events/apilogevent"
	"github.com/go-the-way/unipay/events/orderevent"
	"github.com/go-the-way/unipay/models"
)

// curl -i "https://apilist.tronscanapi.com/api/transfer/trc20?sort=-timestamp&direction=2&db_version=1&trc20Id=TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t&address=TU8fjcJFpgGd2q9roMBmv5c9wo7q2Pwt2d&start=0&limit=1"

var (
	apiUrl    = "https://apilist.tronscanapi.com/api/transfer/trc20?sort=-timestamp&direction=2&db_version=1&trc20Id=TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t&address=%s&start=%d&limit=%d"
	getReqUrl = func(address string, start, limit int) string {
		return fmt.Sprintf(apiUrl, address, start, limit)
	}
)

func startReq(order *models.Order) {
	var (
		page        = 1
		start       = 0
		limit       = 50
		errCount    = 0
		maxErrCount = 3
		sleepDur    = time.Second
		reqTimeout  = time.Second * 3
		client      = &http.Client{Timeout: reqTimeout}
	)
	errLog := func(reqUrl string, err error, statusCode int) *models.ApiLog {
		errCount++
		return models.NewApiLogGetNoParam(reqUrl, err.Error(), fmt.Sprintf("%d", statusCode))
	}
	for {
		reqUrl := getReqUrl(order.Other1, start, limit)
		req, _ := http.NewRequest(http.MethodGet, reqUrl, nil)
		resp, err := client.Do(req)
		var statusCode int
		if err != nil {
			apilogevent.Save(errLog(reqUrl, err, statusCode))
		} else if resp == nil {
			apilogevent.Save(errLog(reqUrl, errors.New("无响应"), statusCode))
		} else {
			statusCode = resp.StatusCode
			buf, readErr := io.ReadAll(resp.Body)
			if readErr != nil {
				apilogevent.Save(errLog(reqUrl, errors.New("读取响应错误"+err.Error()), statusCode))
			} else if len(buf) <= 0 {
				apilogevent.Save(errLog(reqUrl, errors.New("读取响应为空"), statusCode))
			} else {
				if statusCode != http.StatusOK {
					apilogevent.Save(errLog(reqUrl, errors.New("状态码异常:"+string(buf)), statusCode))
				} else {
					var rm respModel
					if err = json.Unmarshal(buf, &rm); err != nil {
						apilogevent.Save(errLog(reqUrl, errors.New("反序列化响应错误："+err.Error()), statusCode))
					} else {
						page++
						if page >= rm.PageSize {
							page = 1
						}
						if len(rm.Data) <= 0 {
							page = 1
						} else {
							// 1709278716000
							timeStamp := rm.Data[0].BlockTimestamp
							orderTimeStamp := pkg.ParseTimeUTC(order.CreateTime).UnixMilli()
							if timeStamp < orderTimeStamp {
								page = 1
							} else {
								if matched := txnFind(order, rm); matched {
									// 找到该订单
									orderevent.Paid(order)
									break
								}
							}
						}
						start = (page - 1) * limit
					}
				}
			}
		}

		if order.CancelTimeBeforeNow() {
			order.Message = "订单超时已被取消"
			orderevent.Expired(order)
			break
		}

		if errCount >= maxErrCount {
			backupevent.Run(order)
			break
		}

		time.Sleep(sleepDur)
	}
}

func txnFind(order *models.Order, rm respModel) (matched bool) {
	for _, tx := range rm.Data {
		// "decimals": 6,
		decimals := tx.Decimals
		orderAmount, _ := strconv.ParseFloat(order.Other2, 64)
		// USD
		amount := int(orderAmount * math.Pow10(decimals))
		// "block_timestamp": 1709278716000,
		txTimeStamp := tx.BlockTimestamp
		txTime := pkg.ParseTime(pkg.FormatTime(time.UnixMilli(txTimeStamp)))
		if tx.To == order.Other1 && fmt.Sprintf("%d", amount) == tx.Amount && tx.ContractRet == "SUCCESS" && order.CreateTimeBeforeTime(txTime) {
			matched = true
			order.TradeId = tx.Hash
			order.PayTime = pkg.FormatTime(txTime)
			break
		}
	}
	return
}

type respModel struct {
	ContractMap map[string]bool `json:"contractMap"`
	TokenInfo   struct {
		TokenId      string `json:"tokenId"`
		TokenAbbr    string `json:"tokenAbbr"`
		TokenName    string `json:"tokenName"`
		TokenDecimal int    `json:"tokenDecimal"`
		TokenCanShow int    `json:"tokenCanShow"`
		TokenType    string `json:"tokenType"`
		TokenLogo    string `json:"tokenLogo"`
		TokenLevel   string `json:"tokenLevel"`
		IssuerAddr   string `json:"issuerAddr"`
		Vip          bool   `json:"vip"`
	} `json:"tokenInfo"`
	PageSize int `json:"page_size"`
	Code     int `json:"code"`
	Data     []struct {
		Amount         string `json:"amount"`
		Status         int    `json:"status"`
		ApprovalAmount string `json:"approval_amount"`
		BlockTimestamp int64  `json:"block_timestamp"`
		Block          int    `json:"block"`
		From           string `json:"from"`
		To             string `json:"to"`
		Hash           string `json:"hash"`
		Confirmed      int    `json:"confirmed"`
		ContractType   string `json:"contract_type"`
		ContractType1  int    `json:"contractType"`
		Revert         int    `json:"revert"`
		ContractRet    string `json:"contract_ret"`
		EventType      string `json:"event_type"`
		IssueAddress   string `json:"issue_address"`
		Decimals       int    `json:"decimals"`
		TokenName      string `json:"token_name"`
		Id             string `json:"id"`
		Direction      int    `json:"direction"`
	} `json:"data"`
}

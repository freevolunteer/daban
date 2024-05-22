#!/bin/bash
#启动行情服务器
nohup ./bin/hqCenter.mac --token=jvquantToken >> log/hqCenter.log.$(date +'%Y-%m-%d') 2>&1 &

#启动交易维护器
nohup ./bin/orderHolder.mac --token=jvquantToken --acc=资金账户 --pwd=资金密码 >> log/orderHolder.log.$(date +'%Y-%m-%d') 2>&1 &

sleep 5
#启动策略,行情服务器需先启动
nohup ./bin/dabanTrigger.mac >> log/dabanTrigger.log.$(date +'%Y-%m-%d') 2>&1 &
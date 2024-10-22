## 工具说明

- 感谢吧友的支持，可转债T0自动交易https://github.com/freevolunteer/bondTrader 反响不错,应大家提议，连夜魔改了一版打板工具。
- 基于可转债项目框架的魔改，hqCenter和orderHolder不做修改，另起一新的策略，关注选定股票的行情变动，临近封板自动触发下单(触发参数自行配置)。
- 本工具只提供辅助，选股和触发策略自己决定，选对股很重要！！！工具只做辅助。
- 暂不考虑自动撤单,排板模式下如果判断没有买入可能，请自行撤单，撤单后自动空出仓位。
- 关于自动排撤单的提议，留着这个功能不做，人工判断更灵活。

## 工具原理
- 券商提供的条件单APP必须保持后台运行，不支持批量股的监测，自己写个性化策略更灵活。
- 热门股或者首板票拉升期临近封板期间，抢筹难度大。
- 手动填好价格参数发单时，车门早被焊死。
- 用程序按规则自动发单速度更快。

### 配置参数解释

```json
{
  //备注
  "comment": "异动观测",
  //最大开仓数，撤单的不算
  "holdCnt": 4,
  //单仓金额
  "amt": 10000,
  //单仓固定手数，如为0则按amt参数计算
  "vol": 0,
  //买单超时时间，超时未成自动撤单
  "bWait": 3600,
  //观测窗口时长
  "sec": 6,
  //最低触发涨幅阈值,一般来说约高越稳，越低利润空间越高
  "raTh": 8,
  //观测期秒均涨幅
  "raRate": 0.125,
  //观测期秒均换手
  "tnRate": 0.005,
  //观测期秒均成交额
  "stockAmt": 20
}
```

## 运行方法
0. 选股，手动选股或者用智能选股筛选，详见pyscript/select.py
1. 09:15运行hqCenter
2. 09:20运行orderHolder
3. 09:25执行策略dabanTrigger
4. 等待观测股的异动，发单抢买。


## 过程干预

- 启停某策略,cid为策略在配置数组里的编号

```go
关闭0号策略 http://127.0.0.1:31866/ctl?cid=0&op=off
启用0号策略 http://127.0.0.1:31866/ctl?cid=0&op=on
```

### 注意事项
- 可以按自己需要修改，源码写得很清楚了。
- 遵循LGPL开源协议，仅用于学习和交流，尊重作者版权，不得开发二次商用！！！
- 有PTrade或其他量化平台权限的，可以魔改移植,须开源。
- 盈亏自负，建议小仓位试错调优参数，调优前勿猛上仓位！！





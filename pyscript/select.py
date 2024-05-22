#!python3
# -*- coding:utf-8 -*-
import argparse
import json

from jvUtil import SQL


def select(token, server="", writeFile=""):
    selectDict = {}
    selectDict['lv1'] = []
    sql = SQL.Construct(token, server)
    names = []

    # 行业圈定,概念或其他筛选条件需调用query方法
    r = sql.industry()
    if 'data' in r and 'list' in r['data'] and "fields" in r['data']:
        field = r["data"]['fields']
        list = r["data"]['list']
        for i in list:
            if len(i) == len(field):
                map = dict(zip(field, i))
                # 按行业信息筛选
                if map["所属行业"] in ["房地产开发", "游戏", "化纤行业", "有色金属", "旅游酒店"]:
                    if map["股票代码"] not in selectDict['lv1']:
                        selectDict['lv1'].append(map["股票代码"])
                        names.append(map["股票简称"])

    # 昨日首板
    r = sql.qeury("主板,非ST,昨日首板")
    if 'data' in r and 'list' in r['data'] and "fields" in r['data']:
        field = r["data"]['fields']
        list = r["data"]['list']
        for i in list:
            if len(i) == len(field):
                map = dict(zip(field, i))
                if map["股票代码"] not in selectDict['lv1']:
                    selectDict['lv1'].append(map["股票代码"])
                    names.append(map["股票简称"])

    print("查询完毕,总计:", len(selectDict['lv1']), selectDict, names)
    # 写入选股文件,供行情订阅
    if writeFile:
        with open(writeFile, 'w') as file:
            file.write(json.dumps(selectDict))
            print("已写入到文件:", writeFile)


# 运行入口
if __name__ == '__main__':
    # 获取命令行参数
    parser = argparse.ArgumentParser(description='筛选股票至结果集,供行情订阅')
    parser.add_argument('--token', type=str, default="")
    parser.add_argument('--server', type=str, default="")  # 可不填
    parser.add_argument('--outFile', type=str, default="")
    args = parser.parse_args()
    token = args.token
    server = args.server
    outFile = args.outFile

    # 筛选
    select(token, server, outFile)

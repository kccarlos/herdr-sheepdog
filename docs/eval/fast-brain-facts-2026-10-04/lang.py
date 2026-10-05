import json,glob,os,re,sys
AB=os.path.dirname(os.path.abspath(__file__))
cjk=re.compile(r'[一-鿿]')
# a few characters that differ between Traditional and Simplified
trad=set('們這個說時間會來對還沒進過頭麼發現問題應該話裡總結論檢錯誤測試開機樣讓給嗎經從當見點將處體實關與為現區無風電訊號')
for arm in sys.argv[1:]:
    en_lines=en_cjk=zh_lines=zh_trad=0
    for rp in glob.glob(os.path.join(AB,arm,'runs','*.json')):
        r=json.load(open(rp))
        ceo=[t['text'] for t in r['transcript'] if t['who']=='ceo']
        zh_ceo=any(cjk.search(x) for x in ceo)
        for t in r['transcript']:
            if t['who']!='sheepdog': continue
            if zh_ceo:
                if cjk.search(t['text']):
                    zh_lines+=1; zh_trad+= any(c in trad for c in t['text'])
            else:
                en_lines+=1; en_cjk+= bool(cjk.search(t['text']))
    print(arm, f"English-speaker lines with Chinese: {en_cjk}/{en_lines}; Chinese lines with Traditional chars: {zh_trad}/{zh_lines}")

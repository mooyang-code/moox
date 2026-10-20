package tencent

// SCFRegion describes a Tencent Cloud SCF region supported by MooX.
type SCFRegion struct {
	Code string
	Name string
	Tag  string
}

var scfRegions = []SCFRegion{
	{Code: "ap-beijing", Name: "北京", Tag: "国内"},
	{Code: "ap-chengdu", Name: "成都", Tag: "国内"},
	{Code: "ap-chongqing", Name: "重庆", Tag: "国内"},
	{Code: "ap-guangzhou", Name: "广州", Tag: "国内"},
	{Code: "ap-nanjing", Name: "南京", Tag: "国内"},
	{Code: "ap-shanghai", Name: "上海", Tag: "国内"},
	{Code: "ap-shanghai-fsi", Name: "上海金融", Tag: "国内"},
	{Code: "ap-shenzhen-fsi", Name: "深圳金融", Tag: "国内"},
	{Code: "ap-hongkong", Name: "中国香港", Tag: "海外"},
	{Code: "na-ashburn", Name: "弗吉尼亚", Tag: "海外"},
	{Code: "na-siliconvalley", Name: "硅谷", Tag: "海外"},
	{Code: "eu-frankfurt", Name: "法兰克福", Tag: "海外"},
	{Code: "ap-singapore", Name: "新加坡", Tag: "海外"},
	{Code: "ap-bangkok", Name: "曼谷", Tag: "海外"},
	{Code: "ap-jakarta", Name: "雅加达", Tag: "海外"},
	{Code: "sa-saopaulo", Name: "圣保罗", Tag: "海外"},
	{Code: "ap-tokyo", Name: "东京", Tag: "海外"},
	{Code: "ap-seoul", Name: "首尔", Tag: "海外"},
}

// SCFRegions returns a copy of the ordered supported-region catalog.
func SCFRegions() []SCFRegion {
	regions := make([]SCFRegion, len(scfRegions))
	copy(regions, scfRegions)
	return regions
}

// IsSCFRegion reports whether code is in MooX's supported SCF catalog.
func IsSCFRegion(code string) bool {
	for _, region := range scfRegions {
		if region.Code == code {
			return true
		}
	}
	return false
}

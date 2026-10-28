//! `#[udf(...)]`：校验声明、生成参数结构体、逐点循环入口、ABI 导出与写进自定义段 `moox.udf` 的元数据。

use proc_macro::TokenStream;
use proc_macro2::Span;
use quote::{format_ident, quote};
use syn::parse::{Parse, ParseStream};
use syn::spanned::Spanned;
use syn::{
    braced, bracketed, parenthesized, parse_macro_input, Expr, ExprLit, ExprRange, Ident, ItemFn,
    Lit, LitStr, RangeLimits, Token, UnOp,
};

const ABI: i32 = 1;
const MAX_INPUTS: usize = 8;
const MAX_PARAMS: usize = 16;

#[derive(Clone, Copy, PartialEq)]
enum Ty {
    Int,
    Float,
}

struct ParamDecl {
    name: Ident,
    ty: Ty,
    min: f64,
    max: f64,
}

struct Args {
    name: LitStr,
    inputs: Vec<Ident>,
    params: Vec<ParamDecl>,
    lookback: LitStr,
    relaxed: bool,
    min_valid: usize,
}

fn number(expr: &Expr) -> syn::Result<f64> {
    match expr {
        Expr::Lit(ExprLit {
            lit: Lit::Int(i), ..
        }) => Ok(i.base10_parse::<i64>()? as f64),
        Expr::Lit(ExprLit {
            lit: Lit::Float(f), ..
        }) => f.base10_parse::<f64>(),
        Expr::Unary(u) if matches!(u.op, UnOp::Neg(_)) => Ok(-number(&u.expr)?),
        Expr::Paren(p) => number(&p.expr),
        _ => Err(syn::Error::new(expr.span(), "范围的端点要是数字字面量")),
    }
}

impl Parse for ParamDecl {
    fn parse(input: ParseStream) -> syn::Result<Self> {
        let name: Ident = input.parse()?;
        input.parse::<Token![:]>()?;
        let kind: Ident = input.parse()?;
        let ty = match kind.to_string().as_str() {
            "int" => Ty::Int,
            "float" => Ty::Float,
            _ => return Err(syn::Error::new(kind.span(), "参数类型只能是 int 或 float")),
        };
        let content;
        parenthesized!(content in input);
        let range: ExprRange = match content.parse::<Expr>()? {
            Expr::Range(r) => r,
            other => return Err(syn::Error::new(other.span(), "参数范围写成 下限..=上限")),
        };
        if !matches!(range.limits, RangeLimits::Closed(_)) {
            return Err(syn::Error::new(
                range.span(),
                "参数范围要写成闭区间 下限..=上限",
            ));
        }
        let (Some(lo), Some(hi)) = (&range.start, &range.end) else {
            return Err(syn::Error::new(
                range.span(),
                "参数范围要同时给出下限和上限",
            ));
        };
        let (min, max) = (number(lo)?, number(hi)?);
        if min > max {
            return Err(syn::Error::new(range.span(), "参数范围的下限大于上限"));
        }
        if ty == Ty::Int && (min != min.trunc() || max != max.trunc()) {
            return Err(syn::Error::new(range.span(), "整数参数的范围要是整数"));
        }
        Ok(ParamDecl { name, ty, min, max })
    }
}

impl Parse for Args {
    fn parse(input: ParseStream) -> syn::Result<Self> {
        let (mut name, mut inputs, mut params, mut lookback, mut nan, mut min_valid) =
            (None, None, None, None, None, None);
        while !input.is_empty() {
            let key: Ident = input.parse()?;
            input.parse::<Token![=]>()?;
            match key.to_string().as_str() {
                "name" => name = Some(input.parse::<LitStr>()?),
                "lookback" => lookback = Some(input.parse::<LitStr>()?),
                "nan" => nan = Some(input.parse::<LitStr>()?),
                "min_valid" => {
                    min_valid = Some(input.parse::<syn::LitInt>()?.base10_parse::<usize>()?)
                }
                "inputs" => {
                    let content;
                    bracketed!(content in input);
                    let list = content.parse_terminated(Ident::parse, Token![,])?;
                    inputs = Some(list.into_iter().collect::<Vec<_>>());
                }
                "params" => {
                    let content;
                    braced!(content in input);
                    let list = content.parse_terminated(ParamDecl::parse, Token![,])?;
                    params = Some(list.into_iter().collect::<Vec<_>>());
                }
                other => return Err(syn::Error::new(key.span(), format!("未知的参数 {other}"))),
            }
            if !input.is_empty() {
                input.parse::<Token![,]>()?;
            }
        }
        let missing = |what: &str| syn::Error::new(Span::call_site(), format!("缺少 {what}"));
        let relaxed = match nan.as_ref().map(|s| s.value()).as_deref() {
            None | Some("strict") => false,
            Some("relaxed") => true,
            Some(_) => {
                return Err(syn::Error::new(
                    nan.unwrap().span(),
                    "nan 取值只能是 \"strict\" 或 \"relaxed\"",
                ))
            }
        };
        let min_valid = min_valid.unwrap_or(1);
        if min_valid < 1 {
            return Err(missing("min_valid ≥ 1"));
        }
        if !relaxed && min_valid != 1 {
            return Err(syn::Error::new(
                Span::call_site(),
                "严格模式不使用 min_valid",
            ));
        }
        Ok(Args {
            name: name.ok_or_else(|| missing("name"))?,
            inputs: inputs.ok_or_else(|| missing("inputs"))?,
            params: params.unwrap_or_default(),
            lookback: lookback.ok_or_else(|| missing("lookback"))?,
            relaxed,
            min_valid,
        })
    }
}

fn valid_ident(s: &str, max: usize) -> bool {
    let mut chars = s.chars();
    matches!(chars.next(), Some('a'..='z'))
        && s.len() <= max
        && chars.all(|c| matches!(c, 'a'..='z' | '0'..='9' | '_'))
}

/// 回看公式：整数常量，或 a*整数参数+b（a、b 为非负整数常量）。返回 (系数, 参数名, 常数项)。
fn parse_lookback(lit: &LitStr, params: &[ParamDecl]) -> syn::Result<(i64, Option<String>, i64)> {
    let err = |msg: String| syn::Error::new(lit.span(), msg);
    let text: String = lit.value().chars().filter(|c| !c.is_whitespace()).collect();
    if text.is_empty() {
        return Err(err("回看公式不能为空".into()));
    }
    let num = |s: &str| -> Option<i64> {
        if s.is_empty() || !s.bytes().all(|b| b.is_ascii_digit()) {
            return None;
        }
        s.parse::<i64>().ok()
    };
    let (rest, b) = match text.rfind('+') {
        Some(i) => match num(&text[i + 1..]) {
            Some(b) if i > 0 => (&text[..i], b),
            _ => {
                return Err(err(format!(
                    "回看公式 {:?} 不合法：加数要是非负整数常量",
                    lit.value()
                )))
            }
        },
        None => (text.as_str(), 0),
    };
    if let Some(c) = num(rest) {
        return Ok((
            0,
            None,
            c.checked_add(b).ok_or_else(|| err("回看公式溢出".into()))?,
        ));
    }
    let (a, name) = match rest.find('*') {
        Some(i) => (
            num(&rest[..i]).ok_or_else(|| {
                err(format!(
                    "回看公式 {:?} 不合法：系数要是非负整数常量",
                    lit.value()
                ))
            })?,
            &rest[i + 1..],
        ),
        None => (1, rest),
    };
    if !valid_ident(name, 32) {
        return Err(err(format!(
            "回看公式 {:?} 不合法：只允许常量或 a*整数参数+b",
            lit.value()
        )));
    }
    match params.iter().find(|p| p.name == name) {
        None => Err(err(format!("回看公式引用了未声明的参数 {name:?}"))),
        Some(p) if p.ty != Ty::Int => Err(err(format!("回看公式引用的参数 {name:?} 要是整数参数"))),
        Some(_) => Ok((a, Some(name.to_string()), b)),
    }
}

fn camel(name: &str) -> String {
    name.split('_')
        .filter(|s| !s.is_empty())
        .map(|s| s[..1].to_uppercase() + &s[1..])
        .collect()
}

fn f64_lit(v: f64) -> proc_macro2::TokenStream {
    // {:?} 保证往返精确；整数值带 .0 才是浮点字面量。
    let text = format!("{v:?}");
    text.parse().unwrap_or_else(|_| quote!(0.0))
}

/// 声明一个自定义算子。
///
/// ```ignore
/// #[udf(name = "kalman_trend", inputs = [x], params = { n: int(2..=500), q: float(0.0..=1.0) }, lookback = "4*n")]
/// fn kalman_trend(x: &Window, p: &KalmanTrendParams) -> f64 { ... }
/// ```
#[proc_macro_attribute]
pub fn udf(attr: TokenStream, item: TokenStream) -> TokenStream {
    let args = parse_macro_input!(attr as Args);
    let func = parse_macro_input!(item as ItemFn);
    match expand(args, func) {
        Ok(ts) => ts.into(),
        Err(e) => e.to_compile_error().into(),
    }
}

fn expand(args: Args, func: ItemFn) -> syn::Result<proc_macro2::TokenStream> {
    let name = args.name.value();
    if !(valid_ident(&name, 48)) {
        return Err(syn::Error::new(
            args.name.span(),
            format!("名称 {name:?} 不合法：要匹配 ^[a-z][a-z0-9_]{{0,47}}$"),
        ));
    }
    if args.inputs.is_empty() || args.inputs.len() > MAX_INPUTS {
        return Err(syn::Error::new(
            Span::call_site(),
            format!("序列输入个数要在 1..={MAX_INPUTS}"),
        ));
    }
    if args.params.len() > MAX_PARAMS {
        return Err(syn::Error::new(
            Span::call_site(),
            format!("参数个数不能超过 {MAX_PARAMS}"),
        ));
    }
    let mut seen = std::collections::HashSet::new();
    for id in args
        .inputs
        .iter()
        .chain(args.params.iter().map(|p| &p.name))
    {
        let s = id.to_string();
        if !valid_ident(&s, 32) {
            return Err(syn::Error::new(
                id.span(),
                format!("名字 {s:?} 不合法：要匹配 ^[a-z][a-z0-9_]{{0,31}}$"),
            ));
        }
        if !seen.insert(s.clone()) {
            return Err(syn::Error::new(id.span(), format!("名字 {s:?} 重复")));
        }
    }
    let (la, lparam, lb) = parse_lookback(&args.lookback, &args.params)?;
    // 回看在参数范围端点的取值：下限处 L ≥ 1。
    let at = |v: f64| -> i64 { la.saturating_mul(v as i64).saturating_add(lb) };
    let lmin = match &lparam {
        Some(p) => at(args
            .params
            .iter()
            .find(|d| d.name == p.as_str())
            .map(|d| d.min)
            .unwrap_or(0.0)),
        None => lb,
    };
    if lmin < 1 {
        return Err(syn::Error::new(
            args.lookback.span(),
            "参数取下限时窗口长度要 ≥ 1",
        ));
    }

    let fn_name = func.sig.ident.clone();
    let params_ty = format_ident!("{}Params", camel(&fn_name.to_string()));
    let n_inputs = args.inputs.len();
    let n_params = args.params.len();
    let relaxed = args.relaxed;
    let min_valid = args.min_valid;

    // 元数据（写进自定义段）：字段顺序固定，序列化结果因此与宿主解析的结构对应。
    let params_json: Vec<String> = args
        .params
        .iter()
        .map(|p| {
            format!(
                "{{\"name\":\"{}\",\"type\":\"{}\",\"min\":{},\"max\":{}}}",
                p.name,
                if p.ty == Ty::Int { "int" } else { "float" },
                json_num(p.min),
                json_num(p.max)
            )
        })
        .collect();
    let inputs_json: Vec<String> = args.inputs.iter().map(|i| format!("\"{i}\"")).collect();
    let manifest = format!(
        "{{\"abi\":{ABI},\"sdk\":\"{}\",\"name\":\"{name}\",\"kind\":\"series\",\"inputs\":[{}],\"params\":[{}],\"lookback\":\"{}\",\"nan\":\"{}\",\"min_valid\":{min_valid}}}",
        env!("CARGO_PKG_VERSION"),
        inputs_json.join(","),
        params_json.join(","),
        args.lookback.value().chars().filter(|c| !c.is_whitespace()).collect::<String>(),
        if relaxed { "relaxed" } else { "strict" },
    );
    let manifest_bytes = proc_macro2::Literal::byte_string(manifest.as_bytes());
    let manifest_len = manifest.len();

    // 参数结构体与解析（越界返回 None → 返回码 1）。
    let fields = args.params.iter().map(|p| {
        let n = &p.name;
        match p.ty {
            Ty::Int => quote!(pub #n: i64),
            Ty::Float => quote!(pub #n: f64),
        }
    });
    let parse_stmts = args.params.iter().enumerate().map(|(i, p)| {
        let n = &p.name;
        let (min, max) = (f64_lit(p.min), f64_lit(p.max));
        match p.ty {
            Ty::Int => quote! {
                let #n = v[#i];
                if !(#n.is_finite() && #n == #n.trunc() && (#min..=#max).contains(&#n)) { return None; }
                let #n = #n as i64;
            },
            Ty::Float => quote! {
                let #n = v[#i];
                if !(#n.is_finite() && (#min..=#max).contains(&#n)) { return None; }
            },
        }
    });
    let field_names: Vec<&Ident> = args.params.iter().map(|p| &p.name).collect();
    let lookback_expr = match &lparam {
        None => quote!(#lb),
        Some(p) => {
            let id = format_ident!("{}", p);
            quote!(#la * p.#id + #lb)
        }
    };
    let window_args = (0..n_inputs).map(|k| quote!(&::moox_udf::Window::new(w[#k])));
    let call_args = if n_params == 0 {
        quote!(#(#window_args),*)
    } else {
        quote!(#(#window_args,)* p)
    };
    let unused_p = if n_params == 0 {
        quote!(let _ = p;)
    } else {
        quote!()
    };
    let parse_fn_body = if n_params == 0 {
        quote!(let _ = v; Some(#params_ty {}))
    } else {
        quote!(#(#parse_stmts)* Some(#params_ty { #(#field_names),* }))
    };

    Ok(quote! {
        #func

        /// 参数结构体（由 `#[udf]` 按 params 声明生成）。
        #[derive(Clone, Copy, Debug)]
        pub struct #params_ty { #(#fields),* }

        #[doc(hidden)]
        #[allow(unsafe_code, non_upper_case_globals, dead_code)]
        mod __moox_udf_export {
            use super::*;

            #[cfg_attr(target_arch = "wasm32", link_section = "moox.udf")]
            #[used]
            static MOOX_UDF_META: [u8; #manifest_len] = *#manifest_bytes;

            pub(super) const SPEC: ::moox_udf::rt::Spec = ::moox_udf::rt::Spec {
                n_inputs: #n_inputs, n_params: #n_params, relaxed: #relaxed, min_valid: #min_valid,
            };

            pub(super) fn parse(v: &[f64]) -> Option<#params_ty> { #parse_fn_body }
            pub(super) fn lookback(p: &#params_ty) -> i64 { #unused_p #lookback_expr }
            pub(super) fn call(w: &[&[f64]], p: &#params_ty) -> f64 { #unused_p #fn_name(#call_args) }
        }

        // ABI 导出只在 wasm 目标上生成：一个 crate 一个 UDF（一个模块、一个版本哈希），
        // 同一个 crate 里的第二个 #[udf] 会因导出符号重复而链接失败；原生测试不需要这些导出。
        #[cfg(target_arch = "wasm32")]
        #[doc(hidden)]
        #[allow(unsafe_code, non_upper_case_globals, dead_code)]
        mod __moox_udf_abi {
            use super::__moox_udf_export::{call, lookback, parse, SPEC};

            #[no_mangle]
            pub extern "C" fn moox_udf_abi() -> i32 { ::moox_udf::rt::ABI_VERSION }

            #[no_mangle]
            pub extern "C" fn moox_udf_reserve(bytes: i32) -> i32 { ::moox_udf::rt::reserve(bytes as u32) as i32 }

            #[no_mangle]
            pub extern "C" fn moox_udf_eval(series: i32, in_len: i32, out_len: i32, params: i32, inputs: i32, out: i32) -> i32 {
                ::moox_udf::rt::eval_ptr(&SPEC, series, in_len, out_len, params, inputs, out, parse, lookback, call)
            }

        }

        /// 在原生环境下运行同一套逐点循环与空值规则（测试用）：布局与 ABI 相同，返回码也相同。
        #[doc(hidden)]
        #[allow(clippy::too_many_arguments)]
        pub fn __moox_udf_native(series: usize, in_len: usize, out_len: usize, params: &[f64], inputs: &[&[f64]], out: &mut [f64]) -> i32 {
            ::moox_udf::rt::eval_slices(&__moox_udf_export::SPEC, series, in_len, out_len, params, inputs, out,
                __moox_udf_export::parse, __moox_udf_export::lookback, __moox_udf_export::call)
        }
    })
}

fn json_num(v: f64) -> String {
    if v == v.trunc() && v.abs() < 1e15 {
        format!("{}", v as i64)
    } else {
        format!("{v:?}")
    }
}

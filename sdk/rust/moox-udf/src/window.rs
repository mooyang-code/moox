/// 以 t 结尾的窗口视图：下标 0 是最老的一个值。越界访问 panic。
///
/// 严格模式下窗口长度就是回看公式给出的 L；`relaxed` 时是各输入的共同有效段（可能短于 L）。
#[derive(Clone, Copy)]
pub struct Window<'a> {
    data: &'a [f64],
}

impl<'a> Window<'a> {
    #[doc(hidden)]
    pub fn new(data: &'a [f64]) -> Self {
        Window { data }
    }

    /// 窗口里的值个数。
    pub fn len(&self) -> usize {
        self.data.len()
    }

    /// 窗口是否为空（只可能出现在 relaxed 且 min_valid 允许时之外的情形不会发生，保留给 clippy）。
    pub fn is_empty(&self) -> bool {
        self.data.is_empty()
    }

    /// 第 i 个值（0 最老）；越界 panic。
    pub fn get(&self, i: usize) -> f64 {
        self.data[i]
    }

    /// 最新的一个值（t 时刻）。
    pub fn last(&self) -> f64 {
        self.data[self.data.len() - 1]
    }

    /// 最老的一个值。
    pub fn first(&self) -> f64 {
        self.data[0]
    }

    /// 从老到新遍历。
    pub fn iter(&self) -> std::iter::Copied<std::slice::Iter<'a, f64>> {
        self.data.iter().copied()
    }

    /// 整个窗口的切片（从老到新）。
    pub fn as_slice(&self) -> &'a [f64] {
        self.data
    }
}

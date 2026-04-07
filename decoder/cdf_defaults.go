// Package decoder implements AV1 bitstream decoding.
//
// This file contains the default CDF tables from AV1 spec Section 9.4.
// Values are sourced from the AV1 reference implementation (libaom
// av1/common/entropymode.c) and the dav1d decoder (src/cdf.c).
//
// Each CDF slice has nsyms+1 entries: the first nsyms-1 are cumulative
// probabilities in descending order, cdf[nsyms-1]=0 (last symbol),
// and cdf[nsyms]=0 (adaptation counter).
package decoder

// defaultKfYModeCDF contains default keyframe Y mode CDFs.
// AV1 spec Section 9.4 (default_kf_y_mode_cdf).
// Indexed by [above_ctx][left_ctx] where ctx = intraModeCtx[mode].
// 5x5 contexts, 13 symbols each.
var defaultKfYModeCDF = [5][5][]uint16{
	{ // above_ctx=0
		{15588, 17027, 19338, 20218, 20682, 21110, 21825, 23244, 24189, 28165, 29093, 30466, 0, 0},
		{12016, 18066, 19516, 20303, 20719, 21444, 21888, 23032, 24434, 28658, 30172, 31409, 0, 0},
		{10052, 10771, 22296, 22788, 23055, 23239, 24133, 25620, 26160, 29336, 29929, 31567, 0, 0},
		{14091, 15406, 16442, 18808, 19136, 19546, 19998, 22096, 24746, 29585, 30958, 32462, 0, 0},
		{12122, 13265, 15603, 16501, 18609, 20033, 22391, 25583, 26437, 30261, 31073, 32475, 0, 0},
	},
	{ // above_ctx=1
		{10023, 19585, 20848, 21440, 21832, 22760, 23089, 24023, 25381, 29014, 30482, 31436, 0, 0},
		{5983, 24099, 24560, 24886, 25066, 25795, 25913, 26423, 27610, 29905, 31276, 31794, 0, 0},
		{7444, 12781, 20177, 20728, 21077, 21607, 22170, 23405, 24469, 27915, 29090, 30492, 0, 0},
		{8537, 14689, 15432, 17087, 17408, 18172, 18408, 19825, 24649, 29153, 31096, 32210, 0, 0},
		{7543, 14231, 15496, 16195, 17905, 20717, 21984, 24516, 26001, 29675, 30981, 31994, 0, 0},
	},
	{ // above_ctx=2
		{12613, 13591, 21383, 22004, 22312, 22577, 23401, 25055, 25729, 29538, 30305, 32077, 0, 0},
		{9687, 13470, 18506, 19230, 19604, 20147, 20695, 22062, 23219, 27743, 29211, 30907, 0, 0},
		{6183, 6505, 26024, 26252, 26366, 26434, 27082, 28354, 28555, 30467, 30794, 32086, 0, 0},
		{10718, 11734, 14954, 17224, 17565, 17924, 18561, 21523, 23878, 28975, 30287, 32252, 0, 0},
		{9194, 9858, 16501, 17263, 18424, 19171, 21563, 25961, 26561, 30072, 30737, 32463, 0, 0},
	},
	{ // above_ctx=3
		{12602, 14399, 15488, 18381, 18778, 19315, 19724, 21419, 25060, 29696, 30917, 32409, 0, 0},
		{8203, 13821, 14524, 17105, 17439, 18131, 18404, 19468, 25225, 29485, 31158, 32342, 0, 0},
		{8451, 9731, 15004, 17643, 18012, 18425, 19070, 21538, 24605, 29118, 30078, 32018, 0, 0},
		{7714, 9048, 9516, 16667, 16817, 16994, 17153, 18767, 26743, 30389, 31536, 32528, 0, 0},
		{8843, 10280, 11496, 15317, 16652, 17943, 19108, 22718, 25769, 29953, 30983, 32485, 0, 0},
	},
	{ // above_ctx=4
		{12578, 13671, 15979, 16834, 19075, 20913, 22989, 25449, 26219, 30214, 31150, 32477, 0, 0},
		{9563, 13626, 15080, 15892, 17756, 20863, 22207, 24236, 25380, 29653, 31143, 32277, 0, 0},
		{8356, 8901, 17616, 18256, 19350, 20106, 22598, 25947, 26466, 29900, 30523, 32261, 0, 0},
		{10835, 11815, 13124, 16042, 17018, 18039, 18947, 22753, 24615, 29489, 30883, 32482, 0, 0},
		{7618, 8288, 9859, 10509, 15386, 18657, 22903, 28776, 29180, 31355, 31802, 32593, 0, 0},
	},
}

// IntraModeCtx maps intra prediction mode to context index for kf_y_mode CDF.
// AV1 spec: {0, 1, 2, 3, 4, 4, 4, 4, 3, 0, 1, 2, 0}.
var IntraModeCtx = [13]int{0, 1, 2, 3, 4, 4, 4, 4, 3, 0, 1, 2, 0}

// defaultPartitionCDF contains default partition CDFs.
// AV1 spec Section 9.4 (default_partition_cdf).
// Indexed by our combined context: offset + neighbor_ctx.
// Matches dav1d block level order: BL_128X128=0, BL_64X64=1, ..., BL_8X8=4.
//
// Contexts 0-3:   128x128 blocks (8 symbols: NONE/H/V/SPLIT + 4 T-shaped).
// Contexts 4-7:   64x64 blocks (10 symbols: all partition types).
// Contexts 8-11:  32x32 blocks (10 symbols).
// Contexts 12-15: 16x16 blocks (10 symbols).
// Contexts 16-19: 8x8 blocks (4 symbols: NONE/H/V/SPLIT).
// Contexts 20-23: unused (uniform fallback).
var defaultPartitionCDF = [24][]uint16{
	// All CDFs in ascending probability format, padded to 11 elements.
	// The clone() function converts to ICDF format (32768 - value).
	// Use 32768 at sentinel/counter/padding positions — clone converts 32768 → 0.
	// Last 2 entries (indices 9-10) are preserved as-is by clone.
	//
	// Contexts 0-3: 128x128 blocks. 8 symbols (nsyms=8).
	// 7 ascending CDF values, then 32768 at [7] (→0 sentinel) and [8] (→0 counter).
	0: []uint16{27899, 28219, 28529, 32484, 32539, 32619, 32639, 32768, 32768, 0, 0},
	1: []uint16{6607, 6990, 8268, 32060, 32219, 32338, 32371, 32768, 32768, 0, 0},
	2: []uint16{5429, 6676, 7122, 32027, 32227, 32531, 32582, 32768, 32768, 0, 0},
	3: []uint16{711, 966, 1172, 32448, 32538, 32617, 32664, 32768, 32768, 0, 0},
	// Contexts 4-7: 64x64 blocks. 10 symbols (nsyms=10).
	// 9 ascending CDF values + sentinel at [9] (preserved 0) + counter at [10] (preserved 0).
	4:  []uint16{20137, 21547, 23078, 29566, 29837, 30261, 30524, 30892, 31724, 0, 0},
	5:  []uint16{6732, 7490, 9497, 27944, 28250, 28515, 28969, 29630, 30104, 0, 0},
	6:  []uint16{5945, 7663, 8348, 28683, 29117, 29749, 30064, 30298, 32238, 0, 0},
	7:  []uint16{870, 1212, 1487, 31198, 31394, 31574, 31743, 31881, 32332, 0, 0},
	// Contexts 8-11: 32x32 blocks. 10 symbols (nsyms=10).
	8:  []uint16{18462, 20920, 23124, 27647, 28227, 29049, 29519, 30178, 31544, 0, 0},
	9:  []uint16{7689, 9060, 12056, 24992, 25660, 26182, 26951, 28041, 29052, 0, 0},
	10: []uint16{6015, 9009, 10062, 24544, 25409, 26545, 27071, 27526, 32047, 0, 0},
	11: []uint16{1394, 2208, 2796, 28614, 29061, 29466, 29840, 30185, 31899, 0, 0},
	// Contexts 12-15: 16x16 blocks. 10 symbols (nsyms=10).
	12: []uint16{15597, 20929, 24571, 26706, 27664, 28821, 29601, 30571, 31902, 0, 0},
	13: []uint16{7925, 11043, 16785, 22470, 23971, 25043, 26651, 28701, 29834, 0, 0},
	14: []uint16{5414, 13269, 15111, 20488, 22360, 24500, 25537, 26336, 32117, 0, 0},
	15: []uint16{2662, 6362, 8614, 20860, 23053, 24778, 26436, 27829, 31171, 0, 0},
	// Contexts 16-19: 8x8 blocks. 4 symbols (nsyms=4).
	// 3 ascending CDF values, then 32768 at [3]-[8] (→0 sentinel, counter, padding).
	16: []uint16{19132, 25510, 30392, 32768, 32768, 32768, 32768, 32768, 32768, 0, 0},
	17: []uint16{13928, 19855, 28540, 32768, 32768, 32768, 32768, 32768, 32768, 0, 0},
	18: []uint16{12522, 23679, 28629, 32768, 32768, 32768, 32768, 32768, 32768, 0, 0},
	19: []uint16{9896, 18783, 25853, 32768, 32768, 32768, 32768, 32768, 32768, 0, 0},
	// Contexts 20-23: unused (4x4 → no further partitioning). Uniform fallback for 4 symbols.
	20: []uint16{24576, 16384, 8192, 32768, 32768, 32768, 32768, 32768, 32768, 0, 0},
	21: []uint16{24576, 16384, 8192, 32768, 32768, 32768, 32768, 32768, 32768, 0, 0},
	22: []uint16{24576, 16384, 8192, 32768, 32768, 32768, 32768, 32768, 32768, 0, 0},
	23: []uint16{24576, 16384, 8192, 32768, 32768, 32768, 32768, 32768, 32768, 0, 0},
}

// defaultSkipCDF contains default skip flag CDFs.
// AV1 spec Section 9.4 (default_skip_txfm_cdfs). 3 contexts, 2 symbols.
var defaultSkipCDF = [3][]uint16{
	0: []uint16{31671, 0, 0},
	1: []uint16{16515, 0, 0},
	2: []uint16{4576, 0, 0},
}

// defaultYModeCDF contains default non-keyframe Y mode CDFs.
// AV1 spec Section 9.4 (default_if_y_mode_cdf). 4 size groups, 13 symbols.
var defaultYModeCDF = [4][]uint16{
	0: []uint16{22801, 23489, 24293, 24756, 25601, 26123, 26606, 27418, 27945, 29228, 29685, 30349, 0, 0},
	1: []uint16{18673, 19845, 22631, 23318, 23950, 24649, 25527, 27364, 28152, 29701, 29984, 30852, 0, 0},
	2: []uint16{19770, 20979, 23396, 23939, 24241, 24654, 25136, 27073, 27830, 29360, 29730, 30659, 0, 0},
	3: []uint16{20155, 21301, 22838, 23178, 23261, 23533, 23703, 24804, 25352, 26575, 27016, 28049, 0, 0},
}

// defaultUVModeCDF contains default UV mode CDFs.
// AV1 spec Section 9.4 (default_uv_mode_cdf).
// [cfl_allowed][y_mode]. cfl_allowed=0: 13 symbols, cfl_allowed=1: 14 symbols.
var defaultUVModeCDF [2][13][]uint16

func init() {
	defaultUVModeCDF[0][0] = []uint16{22631, 24152, 25378, 25661, 25986, 26520, 27055, 27923, 28244, 30059, 30941, 31961, 0, 0}
	defaultUVModeCDF[0][1] = []uint16{9513, 26881, 26973, 27046, 27118, 27664, 27739, 27824, 28359, 29505, 29800, 31796, 0, 0}
	defaultUVModeCDF[0][2] = []uint16{9845, 9915, 28663, 28704, 28757, 28780, 29198, 29822, 29854, 30764, 31777, 32029, 0, 0}
	defaultUVModeCDF[0][3] = []uint16{13639, 13897, 14171, 25331, 25606, 25727, 25953, 27148, 28577, 30612, 31355, 32493, 0, 0}
	defaultUVModeCDF[0][4] = []uint16{9764, 9835, 9930, 9954, 25386, 27053, 27958, 28148, 28243, 31101, 31744, 32363, 0, 0}
	defaultUVModeCDF[0][5] = []uint16{11825, 13589, 13677, 13720, 15048, 29213, 29301, 29458, 29711, 31161, 31441, 32550, 0, 0}
	defaultUVModeCDF[0][6] = []uint16{14175, 14399, 16608, 16821, 17718, 17775, 28551, 30200, 30245, 31837, 32342, 32667, 0, 0}
	defaultUVModeCDF[0][7] = []uint16{12885, 13038, 14978, 15590, 15673, 15748, 16176, 29128, 29267, 30643, 31961, 32461, 0, 0}
	defaultUVModeCDF[0][8] = []uint16{12026, 13661, 13874, 15305, 15490, 15726, 15995, 16273, 28443, 30388, 30767, 32416, 0, 0}
	defaultUVModeCDF[0][9] = []uint16{19052, 19840, 20579, 20916, 21150, 21467, 21885, 22719, 23174, 28861, 30379, 32175, 0, 0}
	defaultUVModeCDF[0][10] = []uint16{18627, 19649, 20974, 21219, 21492, 21816, 22199, 23119, 23527, 27053, 31397, 32148, 0, 0}
	defaultUVModeCDF[0][11] = []uint16{17026, 19004, 19997, 20339, 20586, 21103, 21349, 21907, 22482, 25896, 26541, 31819, 0, 0}
	defaultUVModeCDF[0][12] = []uint16{12124, 13759, 14959, 14992, 15007, 15051, 15078, 15166, 15255, 15753, 16039, 16606, 0, 0}
	defaultUVModeCDF[1][0] = []uint16{10407, 11208, 12900, 13181, 13823, 14175, 14899, 15656, 15986, 20086, 20995, 22455, 24212, 0, 0}
	defaultUVModeCDF[1][1] = []uint16{4532, 19780, 20057, 20215, 20428, 21071, 21199, 21451, 22099, 24228, 24693, 27032, 29472, 0, 0}
	defaultUVModeCDF[1][2] = []uint16{5273, 5379, 20177, 20270, 20385, 20439, 20949, 21695, 21774, 23138, 24256, 24703, 26679, 0, 0}
	defaultUVModeCDF[1][3] = []uint16{6740, 7167, 7662, 14152, 14536, 14785, 15034, 16741, 18371, 21520, 22206, 23389, 24182, 0, 0}
	defaultUVModeCDF[1][4] = []uint16{4987, 5368, 5928, 6068, 19114, 20315, 21857, 22253, 22411, 24911, 25380, 26027, 26376, 0, 0}
	defaultUVModeCDF[1][5] = []uint16{5370, 6889, 7247, 7393, 9498, 21114, 21402, 21753, 21981, 24780, 25386, 26517, 27176, 0, 0}
	defaultUVModeCDF[1][6] = []uint16{4816, 4961, 7204, 7326, 8765, 8930, 20169, 20682, 20803, 23188, 23763, 24455, 24940, 0, 0}
	defaultUVModeCDF[1][7] = []uint16{6608, 6740, 8529, 9049, 9257, 9356, 9735, 18827, 19059, 22336, 23204, 23964, 24793, 0, 0}
	defaultUVModeCDF[1][8] = []uint16{5998, 7419, 7781, 8933, 9255, 9549, 9753, 10417, 18898, 22494, 23139, 24764, 25989, 0, 0}
	defaultUVModeCDF[1][9] = []uint16{10660, 11298, 12550, 12957, 13322, 13624, 14040, 15004, 15534, 20714, 21789, 23443, 24861, 0, 0}
	defaultUVModeCDF[1][10] = []uint16{10522, 11530, 12552, 12963, 13378, 13779, 14245, 15235, 15902, 20102, 22696, 23774, 25838, 0, 0}
	defaultUVModeCDF[1][11] = []uint16{10099, 10691, 12639, 13049, 13386, 13665, 14125, 15163, 15636, 19676, 20474, 23519, 25208, 0, 0}
	defaultUVModeCDF[1][12] = []uint16{3144, 5087, 7382, 7504, 7593, 7690, 7801, 8064, 8232, 9248, 9875, 10521, 29048, 0, 0}
}

// defaultAngleDeltaCDF contains default angle delta CDFs.
// AV1 spec Section 9.4. 8 directional modes, 7 symbols each.
var defaultAngleDeltaCDF = [8][]uint16{
	0: []uint16{2180, 5032, 7567, 22776, 26989, 30217, 0, 0},
	1: []uint16{2301, 5608, 8801, 23487, 26974, 30330, 0, 0},
	2: []uint16{3780, 11018, 13699, 19354, 23083, 31286, 0, 0},
	3: []uint16{4581, 11226, 15147, 17138, 21834, 28397, 0, 0},
	4: []uint16{1737, 10927, 14509, 19588, 22745, 28823, 0, 0},
	5: []uint16{2664, 10176, 12485, 17650, 21600, 30495, 0, 0},
	6: []uint16{2240, 11096, 15453, 20341, 22561, 28917, 0, 0},
	7: []uint16{3605, 10428, 12459, 17676, 21244, 30655, 0, 0},
}

// defaultTxSizeCDF contains default TX size CDFs.
// AV1 spec Section 9.4. [max_tx_cat][tx_size_ctx].
var defaultTxSizeCDF = [4][3][]uint16{
	0: {
		[]uint16{19968, 0, 0},
		[]uint16{19968, 0, 0},
		[]uint16{24320, 0, 0},
	},
	1: {
		[]uint16{12272, 30172, 0, 0},
		[]uint16{12272, 30172, 0, 0},
		[]uint16{18677, 30848, 0, 0},
	},
	2: {
		[]uint16{12986, 15180, 0, 0},
		[]uint16{12986, 15180, 0, 0},
		[]uint16{24302, 25602, 0, 0},
	},
	3: {
		[]uint16{5782, 11475, 0, 0},
		[]uint16{5782, 11475, 0, 0},
		[]uint16{16803, 22759, 0, 0},
	},
}

// defaultIsInterCDF contains default intra/inter CDFs.
// AV1 spec Section 9.4. 4 contexts, 2 symbols.
var defaultIsInterCDF = [4][]uint16{
	0: []uint16{806, 0, 0},
	1: []uint16{16662, 0, 0},
	2: []uint16{20186, 0, 0},
	3: []uint16{26538, 0, 0},
}

// defaultSkipModeCDF contains default skip mode CDFs.
// AV1 spec Section 9.4. 3 contexts, 2 symbols.
var defaultSkipModeCDF = [3][]uint16{
	0: []uint16{32621, 0, 0},
	1: []uint16{20708, 0, 0},
	2: []uint16{8127, 0, 0},
}

// defaultDeltaQCDF contains default delta Q CDF. 4 symbols.
var defaultDeltaQCDF = []uint16{28160, 32120, 32677, 0, 0}

// defaultDeltaLFCDF contains default delta LF CDF. 4 symbols.
var defaultDeltaLFCDF = []uint16{28160, 32120, 32677, 0, 0}

// defaultDeltaLFMultiCDF contains default multi delta LF CDFs. 4 symbols each.
var defaultDeltaLFMultiCDF = [4][]uint16{
	0: []uint16{28160, 32120, 32677, 0, 0},
	1: []uint16{28160, 32120, 32677, 0, 0},
	2: []uint16{28160, 32120, 32677, 0, 0},
	3: []uint16{28160, 32120, 32677, 0, 0},
}

// defaultNewMVCDF contains default NewMV CDFs. 6 contexts, 2 symbols.
var defaultNewMVCDF = [6][]uint16{
	0: []uint16{24035, 0, 0},
	1: []uint16{16630, 0, 0},
	2: []uint16{15339, 0, 0},
	3: []uint16{8386, 0, 0},
	4: []uint16{12222, 0, 0},
	5: []uint16{4676, 0, 0},
}

// defaultZeroMVCDF contains default ZeroMV CDFs. 2 contexts, 2 symbols.
var defaultZeroMVCDF = [2][]uint16{
	0: []uint16{2175, 0, 0},
	1: []uint16{1054, 0, 0},
}

// defaultRefMVCDF contains default RefMV CDFs. 6 contexts, 2 symbols.
var defaultRefMVCDF = [6][]uint16{
	0: []uint16{23974, 0, 0},
	1: []uint16{24188, 0, 0},
	2: []uint16{17848, 0, 0},
	3: []uint16{28622, 0, 0},
	4: []uint16{24312, 0, 0},
	5: []uint16{19923, 0, 0},
}

// defaultDrlModeCDF contains default DRL mode CDFs. 3 contexts, 2 symbols.
var defaultDrlModeCDF = [3][]uint16{
	0: []uint16{13104, 0, 0},
	1: []uint16{24560, 0, 0},
	2: []uint16{18945, 0, 0},
}

// defaultCompoundModeCDF contains default compound mode CDFs. 8 contexts, 8 symbols.
var defaultCompoundModeCDF = [8][]uint16{
	0: []uint16{7760, 13823, 15808, 17641, 19156, 20666, 26891, 0, 0},
	1: []uint16{10730, 19452, 21145, 22749, 24039, 25131, 28724, 0, 0},
	2: []uint16{10664, 20221, 21588, 22906, 24295, 25387, 28436, 0, 0},
	3: []uint16{13298, 16984, 20471, 24182, 25067, 25736, 26422, 0, 0},
	4: []uint16{18904, 23325, 25242, 27432, 27898, 28258, 30758, 0, 0},
	5: []uint16{10725, 17454, 20124, 22820, 24195, 25168, 26046, 0, 0},
	6: []uint16{17125, 24273, 25814, 27492, 28214, 28704, 30592, 0, 0},
	7: []uint16{13046, 23214, 24505, 25942, 27435, 28442, 29330, 0, 0},
}

// defaultSingleRefCDF contains default single ref CDFs.
// AV1 spec Section 9.4. [level][ctx], 2 symbols.
// Matches dav1d's ref[6][3] layout: 6 decision levels, 3 contexts each.
var defaultSingleRefCDF = [6][3][]uint16{
	0: { // Level 0: fwd vs bwd (single_ref_p1)
		[]uint16{4897, 0, 0},
		[]uint16{16973, 0, 0},
		[]uint16{29744, 0, 0},
	},
	1: { // Level 1: bwd ref bit (single_ref_p2)
		[]uint16{1555, 0, 0},
		[]uint16{16751, 0, 0},
		[]uint16{30279, 0, 0},
	},
	2: { // Level 2: fwd upper/lower (single_ref_p3)
		[]uint16{4236, 0, 0},
		[]uint16{19647, 0, 0},
		[]uint16{31194, 0, 0},
	},
	3: { // Level 3: fwd lower detail (single_ref_p4)
		[]uint16{8650, 0, 0},
		[]uint16{24773, 0, 0},
		[]uint16{31895, 0, 0},
	},
	4: { // Level 4: fwd upper detail (single_ref_p5)
		[]uint16{904, 0, 0},
		[]uint16{11014, 0, 0},
		[]uint16{26875, 0, 0},
	},
	5: { // Level 5: bwd detail (single_ref_p6)
		[]uint16{1444, 0, 0},
		[]uint16{15087, 0, 0},
		[]uint16{30304, 0, 0},
	},
}

// defaultCompRefCDF contains default compound ref CDFs.
// AV1 spec Section 9.4. [ref_type][ctx], 2 symbols.
var defaultCompRefCDF = [3][6][]uint16{
	0: {
		[]uint16{4946, 0, 0},
		[]uint16{19891, 0, 0},
		[]uint16{30731, 0, 0},
		[]uint16{16384, 0, 0},
		[]uint16{16384, 0, 0},
		[]uint16{16384, 0, 0},
	},
	1: {
		[]uint16{9468, 0, 0},
		[]uint16{22441, 0, 0},
		[]uint16{31059, 0, 0},
		[]uint16{16384, 0, 0},
		[]uint16{16384, 0, 0},
		[]uint16{16384, 0, 0},
	},
	2: {
		[]uint16{1503, 0, 0},
		[]uint16{15160, 0, 0},
		[]uint16{27544, 0, 0},
		[]uint16{16384, 0, 0},
		[]uint16{16384, 0, 0},
		[]uint16{16384, 0, 0},
	},
}

// defaultCompBwdRefCDF contains default compound backward ref CDFs.
// Matches dav1d's comp_bwd_ref[2][3]. [level][ctx], 2 symbols.
var defaultCompBwdRefCDF = [2][3][]uint16{
	0: {
		[]uint16{2235, 0, 0},
		[]uint16{17182, 0, 0},
		[]uint16{30606, 0, 0},
	},
	1: {
		[]uint16{1423, 0, 0},
		[]uint16{15175, 0, 0},
		[]uint16{30489, 0, 0},
	},
}

// defaultCompUniRefCDF contains default compound unidir ref CDFs.
// Matches dav1d's comp_uni_ref[3][3]. [level][ctx], 2 symbols.
var defaultCompUniRefCDF = [3][3][]uint16{
	0: {
		[]uint16{5284, 0, 0},
		[]uint16{23152, 0, 0},
		[]uint16{31774, 0, 0},
	},
	1: {
		[]uint16{3865, 0, 0},
		[]uint16{14173, 0, 0},
		[]uint16{25120, 0, 0},
	},
	2: {
		[]uint16{3128, 0, 0},
		[]uint16{15270, 0, 0},
		[]uint16{26710, 0, 0},
	},
}

// defaultReferenceModeCDF contains default reference mode CDFs.
// AV1 spec Section 9.4. 5 contexts, 2 symbols.
var defaultReferenceModeCDF = [5][]uint16{
	0: []uint16{26828, 0, 0},
	1: []uint16{24035, 0, 0},
	2: []uint16{12031, 0, 0},
	3: []uint16{10640, 0, 0},
	4: []uint16{2901, 0, 0},
}

// defaultCompReferenceTypeCDF contains default compound reference type CDFs.
// AV1 spec Section 9.4. 5 contexts, 2 symbols.
var defaultCompReferenceTypeCDF = [5][]uint16{
	0: []uint16{1198, 0, 0},
	1: []uint16{2070, 0, 0},
	2: []uint16{9166, 0, 0},
	3: []uint16{7499, 0, 0},
	4: []uint16{22475, 0, 0},
}

// defaultMotionModeCDF contains default motion mode CDFs.
// AV1 spec Section 9.4. [block_size], 3 symbols.
var defaultMotionModeCDF = [22][]uint16{
	0: []uint16{10923, 21845, 0, 0},
	1: []uint16{10923, 21845, 0, 0},
	2: []uint16{10923, 21845, 0, 0},
	3: []uint16{7651, 24760, 0, 0},
	4: []uint16{4738, 24765, 0, 0},
	5: []uint16{5391, 25528, 0, 0},
	6: []uint16{19419, 26810, 0, 0},
	7: []uint16{5123, 23606, 0, 0},
	8: []uint16{11606, 24308, 0, 0},
	9: []uint16{26260, 29116, 0, 0},
	10: []uint16{20360, 28062, 0, 0},
	11: []uint16{21679, 26830, 0, 0},
	12: []uint16{29516, 30701, 0, 0},
	13: []uint16{28898, 30397, 0, 0},
	14: []uint16{30878, 31335, 0, 0},
	15: []uint16{32507, 32558, 0, 0},
	16: []uint16{10923, 21845, 0, 0},
	17: []uint16{10923, 21845, 0, 0},
	18: []uint16{28799, 31390, 0, 0},
	19: []uint16{26431, 30774, 0, 0},
	20: []uint16{28973, 31594, 0, 0},
	21: []uint16{29742, 31203, 0, 0},
}

// defaultOBMCCDF contains default OBMC CDFs (boolean, separate from motion_mode).
// From dav1d cdf.c .obmc table, mapped to our block size enum.
// [block_size], 2 symbols.
var defaultOBMCCDF = [22][]uint16{
	0:  {16384, 0, 0}, // BLOCK_4X4 (unused)
	1:  {16384, 0, 0}, // BLOCK_4X8 (unused)
	2:  {16384, 0, 0}, // BLOCK_8X4 (unused)
	3:  {10437, 0, 0}, // BLOCK_8X8
	4:  {9371, 0, 0},  // BLOCK_8X16
	5:  {9301, 0, 0},  // BLOCK_16X8
	6:  {17432, 0, 0}, // BLOCK_16X16
	7:  {14423, 0, 0}, // BLOCK_16X32
	8:  {15142, 0, 0}, // BLOCK_32X16
	9:  {25817, 0, 0}, // BLOCK_32X32
	10: {22823, 0, 0}, // BLOCK_32X64
	11: {22083, 0, 0}, // BLOCK_64X32
	12: {30128, 0, 0}, // BLOCK_64X64
	13: {31014, 0, 0}, // BLOCK_64X128
	14: {31560, 0, 0}, // BLOCK_128X64
	15: {32638, 0, 0}, // BLOCK_128X128
	16: {16384, 0, 0}, // BLOCK_4X16 (unused)
	17: {16384, 0, 0}, // BLOCK_16X4 (unused)
	18: {23664, 0, 0}, // BLOCK_8X32
	19: {20901, 0, 0}, // BLOCK_32X8
	20: {24008, 0, 0}, // BLOCK_16X64
	21: {26879, 0, 0}, // BLOCK_64X16
}

// defaultInterIntraCDF contains default inter-intra CDFs.
// AV1 spec Section 9.4. [bsize_group], 2 symbols.
var defaultInterIntraCDF = [4][]uint16{
	0: []uint16{16384, 0, 0},
	1: []uint16{26887, 0, 0},
	2: []uint16{27597, 0, 0},
	3: []uint16{30237, 0, 0},
}

// defaultInterIntraModeCDF contains default inter-intra mode CDFs.
// AV1 spec Section 9.4. [bsize_group], 4 symbols.
var defaultInterIntraModeCDF = [4][]uint16{
	0: []uint16{8192, 16384, 24576, 0, 0},
	1: []uint16{1875, 11082, 27332, 0, 0},
	2: []uint16{2473, 9996, 26388, 0, 0},
	3: []uint16{4238, 11537, 25926, 0, 0},
}

// defaultWedgeInterIntraCDF contains default wedge inter-intra CDFs.
// AV1 spec Section 9.4. [bsize], 2 symbols.
var defaultWedgeInterIntraCDF = [22][]uint16{
	0: []uint16{16384, 0, 0},
	1: []uint16{16384, 0, 0},
	2: []uint16{16384, 0, 0},
	3: []uint16{20036, 0, 0},
	4: []uint16{24957, 0, 0},
	5: []uint16{26704, 0, 0},
	6: []uint16{27530, 0, 0},
	7: []uint16{29564, 0, 0},
	8: []uint16{29444, 0, 0},
	9: []uint16{26872, 0, 0},
	10: []uint16{16384, 0, 0},
	11: []uint16{16384, 0, 0},
	12: []uint16{16384, 0, 0},
	13: []uint16{16384, 0, 0},
	14: []uint16{16384, 0, 0},
	15: []uint16{16384, 0, 0},
	16: []uint16{16384, 0, 0},
	17: []uint16{16384, 0, 0},
	18: []uint16{16384, 0, 0},
	19: []uint16{16384, 0, 0},
	20: []uint16{16384, 0, 0},
	21: []uint16{16384, 0, 0},
}

// defaultCompoundTypeCDF contains default compound type CDFs.
// AV1 spec Section 9.4. [bsize], 2 symbols.
var defaultCompoundTypeCDF = [22][]uint16{
	0: []uint16{16384, 0, 0},
	1: []uint16{16384, 0, 0},
	2: []uint16{16384, 0, 0},
	3: []uint16{23431, 0, 0},
	4: []uint16{13171, 0, 0},
	5: []uint16{11470, 0, 0},
	6: []uint16{9770, 0, 0},
	7: []uint16{9100, 0, 0},
	8: []uint16{8233, 0, 0},
	9: []uint16{6172, 0, 0},
	10: []uint16{16384, 0, 0},
	11: []uint16{16384, 0, 0},
	12: []uint16{16384, 0, 0},
	13: []uint16{16384, 0, 0},
	14: []uint16{16384, 0, 0},
	15: []uint16{16384, 0, 0},
	16: []uint16{16384, 0, 0},
	17: []uint16{16384, 0, 0},
	18: []uint16{11820, 0, 0},
	19: []uint16{7701, 0, 0},
	20: []uint16{16384, 0, 0},
	21: []uint16{16384, 0, 0},
}

// defaultCompoundIdxCDF contains default compound index CDFs.
// AV1 spec Section 9.4. [ctx], 2 symbols.
var defaultCompoundIdxCDF = [6][]uint16{
	0: []uint16{18244, 0, 0},
	1: []uint16{12865, 0, 0},
	2: []uint16{7053, 0, 0},
	3: []uint16{13259, 0, 0},
	4: []uint16{9334, 0, 0},
	5: []uint16{4644, 0, 0},
}

// defaultCompGroupIdxCDF contains default comp group idx CDFs.
// AV1 spec Section 9.4. [ctx], 2 symbols.
var defaultCompGroupIdxCDF = [6][]uint16{
	0: []uint16{26607, 0, 0},
	1: []uint16{22891, 0, 0},
	2: []uint16{18840, 0, 0},
	3: []uint16{24594, 0, 0},
	4: []uint16{19934, 0, 0},
	5: []uint16{22674, 0, 0},
}

// defaultMVJointCDF contains default MV joint CDF. 4 symbols.
var defaultMVJointCDF = []uint16{4096, 11264, 19328, 0, 0}

// defaultMVSignCDF contains default MV sign CDFs. [comp], 2 symbols.
var defaultMVSignCDF = [2][]uint16{
	0: []uint16{16384, 0, 0},
	1: []uint16{16384, 0, 0},
}

// defaultMVClassCDF contains default MV class CDFs. [comp], 11 symbols.
var defaultMVClassCDF = [2][]uint16{
	0: []uint16{28672, 30976, 31858, 32320, 32551, 32656, 32740, 32757, 32762, 32767, 0, 0},
	1: []uint16{28672, 30976, 31858, 32320, 32551, 32656, 32740, 32757, 32762, 32767, 0, 0},
}

// defaultMVClass0BitCDF contains default MV class0 bit CDFs. [comp], 2 symbols.
var defaultMVClass0BitCDF = [2][]uint16{
	0: []uint16{27648, 0, 0},
	1: []uint16{27648, 0, 0},
}

// defaultMVClass0FRCDF contains default MV class0 FR CDFs. [comp][class0_bit], 4 symbols.
var defaultMVClass0FRCDF = [2][2][]uint16{
	0: {
		[]uint16{16384, 24576, 26624, 0, 0},
		[]uint16{12288, 21248, 24128, 0, 0},
	},
	1: {
		[]uint16{16384, 24576, 26624, 0, 0},
		[]uint16{12288, 21248, 24128, 0, 0},
	},
}

// defaultMVClass0HPCDF contains default MV class0 HP CDFs. [comp], 2 symbols.
var defaultMVClass0HPCDF = [2][]uint16{
	0: []uint16{20480, 0, 0},
	1: []uint16{20480, 0, 0},
}

// defaultMVBitCDF contains default MV bit CDFs. [comp][bit_pos], 2 symbols.
var defaultMVBitCDF = [2][10][]uint16{
	0: {
		[]uint16{17408, 0, 0},
		[]uint16{17920, 0, 0},
		[]uint16{18944, 0, 0},
		[]uint16{20480, 0, 0},
		[]uint16{22528, 0, 0},
		[]uint16{24576, 0, 0},
		[]uint16{28672, 0, 0},
		[]uint16{29952, 0, 0},
		[]uint16{29952, 0, 0},
		[]uint16{30720, 0, 0},
	},
	1: {
		[]uint16{17408, 0, 0},
		[]uint16{17920, 0, 0},
		[]uint16{18944, 0, 0},
		[]uint16{20480, 0, 0},
		[]uint16{22528, 0, 0},
		[]uint16{24576, 0, 0},
		[]uint16{28672, 0, 0},
		[]uint16{29952, 0, 0},
		[]uint16{29952, 0, 0},
		[]uint16{30720, 0, 0},
	},
}

// defaultMVFRCDF contains default MV FR CDFs. [comp], 4 symbols.
var defaultMVFRCDF = [2][]uint16{
	0: []uint16{8192, 17408, 21248, 0, 0},
	1: []uint16{8192, 17408, 21248, 0, 0},
}

// defaultMVHPCDF contains default MV HP CDFs. [comp], 2 symbols.
var defaultMVHPCDF = [2][]uint16{
	0: []uint16{16384, 0, 0},
	1: []uint16{16384, 0, 0},
}

// defaultSwitchableFilterCDF contains default switchable interpolation filter CDFs.
// AV1 spec Section 9.4. [dim][ctx], 3 symbols.
// Values from dav1d cdf.c default CDF tables.
var defaultSwitchableFilterCDF = [2][8][]uint16{
	{ // dim 0
		{31935, 32720, 0, 0}, {5568, 32719, 0, 0},
		{422, 2938, 0, 0}, {28244, 32608, 0, 0},
		{31206, 31953, 0, 0}, {4862, 32121, 0, 0},
		{770, 1152, 0, 0}, {20889, 25637, 0, 0},
	},
	{ // dim 1
		{31910, 32724, 0, 0}, {4120, 32712, 0, 0},
		{305, 2247, 0, 0}, {27403, 32636, 0, 0},
		{31022, 32009, 0, 0}, {2963, 32093, 0, 0},
		{601, 943, 0, 0}, {14969, 21398, 0, 0},
	},
}

// defaultFilterIntraModeCDF contains default filter intra mode CDF. 5 symbols.
var defaultFilterIntraModeCDF = []uint16{8949, 12776, 17211, 29558, 0, 0}

// defaultUseFilterIntraCDF contains default use filter intra CDFs.
// AV1 spec Section 9.4. [block_size], 2 symbols.
var defaultUseFilterIntraCDF = [22][]uint16{
	0: []uint16{4621, 0, 0},
	1: []uint16{6743, 0, 0},
	2: []uint16{5893, 0, 0},
	3: []uint16{7866, 0, 0},
	4: []uint16{12551, 0, 0},
	5: []uint16{9394, 0, 0},
	6: []uint16{12408, 0, 0},
	7: []uint16{14301, 0, 0},
	8: []uint16{12756, 0, 0},
	9: []uint16{22343, 0, 0},
	10: []uint16{16384, 0, 0},
	11: []uint16{16384, 0, 0},
	12: []uint16{16384, 0, 0},
	13: []uint16{16384, 0, 0},
	14: []uint16{16384, 0, 0},
	15: []uint16{16384, 0, 0},
	16: []uint16{12770, 0, 0},
	17: []uint16{10368, 0, 0},
	18: []uint16{20229, 0, 0},
	19: []uint16{18101, 0, 0},
	20: []uint16{16384, 0, 0},
	21: []uint16{16384, 0, 0},
}

// defaultPaletteYModeCDF contains default palette Y mode CDFs.
// AV1 spec Section 9.4. [bsize_ctx][palette_ctx], 2 symbols.
var defaultPaletteYModeCDF = [7][3][]uint16{
	0: {
		[]uint16{31676, 0, 0},
		[]uint16{3419, 0, 0},
		[]uint16{1261, 0, 0},
	},
	1: {
		[]uint16{31912, 0, 0},
		[]uint16{2859, 0, 0},
		[]uint16{980, 0, 0},
	},
	2: {
		[]uint16{31823, 0, 0},
		[]uint16{3400, 0, 0},
		[]uint16{781, 0, 0},
	},
	3: {
		[]uint16{32030, 0, 0},
		[]uint16{3561, 0, 0},
		[]uint16{904, 0, 0},
	},
	4: {
		[]uint16{32309, 0, 0},
		[]uint16{7337, 0, 0},
		[]uint16{1462, 0, 0},
	},
	5: {
		[]uint16{32265, 0, 0},
		[]uint16{4015, 0, 0},
		[]uint16{1521, 0, 0},
	},
	6: {
		[]uint16{32450, 0, 0},
		[]uint16{7946, 0, 0},
		[]uint16{129, 0, 0},
	},
}

// defaultPaletteUVModeCDF contains default palette UV mode CDFs.
// AV1 spec Section 9.4. [palette_uv_ctx], 2 symbols.
var defaultPaletteUVModeCDF = [2][]uint16{
	0: []uint16{32461, 0, 0},
	1: []uint16{21488, 0, 0},
}

// defaultPaletteSzCDF contains default palette size CDFs.
// [plane][sz_ctx], 7 symbols (palette sizes 2-8). From dav1d cdf.c.
var defaultPaletteSzCDF = [2][7][]uint16{
	{ // Y plane
		{7952, 13000, 18149, 21478, 25527, 29241, 0, 0},
		{7139, 11421, 16195, 19544, 23666, 28073, 0, 0},
		{7788, 12741, 17325, 20500, 24315, 28530, 0, 0},
		{8271, 14064, 18246, 21564, 25071, 28533, 0, 0},
		{12725, 19180, 21863, 24839, 27535, 30120, 0, 0},
		{9711, 14888, 16923, 21052, 25661, 27875, 0, 0},
		{14940, 20797, 21678, 24186, 27033, 28999, 0, 0},
	},
	{ // UV plane
		{8713, 19979, 27128, 29609, 31331, 32272, 0, 0},
		{5839, 15573, 23581, 26947, 29848, 31700, 0, 0},
		{4426, 11260, 17999, 21483, 25863, 29430, 0, 0},
		{3228, 9464, 14993, 18089, 22523, 27420, 0, 0},
		{3768, 8886, 13091, 17852, 22495, 27207, 0, 0},
		{2464, 8451, 12861, 21632, 25525, 28555, 0, 0},
		{1269, 5435, 10433, 18963, 21700, 25865, 0, 0},
	},
}

// defaultColorMapCDF contains default color map CDFs.
// [plane][pal_sz-2][ctx], variable symbols. From dav1d cdf.c.
var defaultColorMapCDF = [2][7][5][]uint16{
	{ // Y plane
		{ // pal_sz=2 (1 symbol)
			{28710, 0, 0}, {16384, 0, 0}, {10553, 0, 0}, {27036, 0, 0}, {31603, 0, 0},
		},
		{ // pal_sz=3 (2 symbols)
			{27877, 30490, 0, 0}, {11532, 25697, 0, 0}, {6544, 30234, 0, 0}, {23018, 28072, 0, 0}, {31915, 32385, 0, 0},
		},
		{ // pal_sz=4 (3 symbols)
			{25572, 28046, 30045, 0, 0}, {9478, 21590, 27256, 0, 0}, {7248, 26837, 29824, 0, 0}, {19167, 24486, 28349, 0, 0}, {31400, 31825, 32250, 0, 0},
		},
		{ // pal_sz=5 (4 symbols)
			{24779, 26955, 28576, 30282, 0, 0}, {8669, 20364, 24073, 28093, 0, 0}, {4255, 27565, 29377, 31067, 0, 0}, {19864, 23674, 26716, 29530, 0, 0}, {31646, 31893, 32147, 32426, 0, 0},
		},
		{ // pal_sz=6 (5 symbols)
			{23132, 25407, 26970, 28435, 30073, 0, 0}, {7443, 17242, 20717, 24762, 27982, 0, 0}, {6300, 24862, 26944, 28784, 30671, 0, 0}, {18916, 22895, 25267, 27435, 29652, 0, 0}, {31270, 31550, 31808, 32059, 32353, 0, 0},
		},
		{ // pal_sz=7 (6 symbols)
			{23105, 25199, 26464, 27684, 28931, 30318, 0, 0}, {6950, 15447, 18952, 22681, 25567, 28563, 0, 0}, {7560, 23474, 25490, 27203, 28921, 30708, 0, 0}, {18544, 22373, 24457, 26195, 28119, 30045, 0, 0}, {31198, 31451, 31670, 31882, 32123, 32391, 0, 0},
		},
		{ // pal_sz=8 (7 symbols)
			{21689, 23883, 25163, 26352, 27506, 28827, 30195, 0, 0}, {6892, 15385, 17840, 21606, 24287, 26753, 29204, 0, 0}, {5651, 23182, 25042, 26518, 27982, 29392, 30900, 0, 0}, {19349, 22578, 24418, 25994, 27524, 29031, 30448, 0, 0}, {31028, 31270, 31504, 31705, 31927, 32153, 32392, 0, 0},
		},
	},
	{ // UV plane
		{ // pal_sz=2 (1 symbol)
			{29089, 0, 0}, {16384, 0, 0}, {8713, 0, 0}, {29257, 0, 0}, {31610, 0, 0},
		},
		{ // pal_sz=3 (2 symbols)
			{25257, 29145, 0, 0}, {12287, 27293, 0, 0}, {7033, 27960, 0, 0}, {20145, 25405, 0, 0}, {30608, 31639, 0, 0},
		},
		{ // pal_sz=4 (3 symbols)
			{24210, 27175, 29903, 0, 0}, {9888, 22386, 27214, 0, 0}, {5901, 26053, 29293, 0, 0}, {18318, 22152, 28333, 0, 0}, {30459, 31136, 31926, 0, 0},
		},
		{ // pal_sz=5 (4 symbols)
			{22980, 25479, 27781, 29986, 0, 0}, {8413, 21408, 24859, 28874, 0, 0}, {2257, 29449, 30594, 31598, 0, 0}, {19189, 21202, 25915, 28620, 0, 0}, {31844, 32044, 32281, 32518, 0, 0},
		},
		{ // pal_sz=6 (5 symbols)
			{22217, 24567, 26637, 28683, 30548, 0, 0}, {7307, 16406, 19636, 24632, 28424, 0, 0}, {4441, 25064, 26879, 28942, 30919, 0, 0}, {17210, 20528, 23319, 26750, 29582, 0, 0}, {30674, 30953, 31396, 31735, 32207, 0, 0},
		},
		{ // pal_sz=7 (6 symbols)
			{21239, 23168, 25044, 26962, 28705, 30506, 0, 0}, {6545, 15012, 18004, 21817, 25503, 28701, 0, 0}, {3448, 26295, 27437, 28704, 30126, 31442, 0, 0}, {15889, 18323, 21704, 24698, 26976, 29690, 0, 0}, {30988, 31204, 31479, 31734, 31983, 32325, 0, 0},
		},
		{ // pal_sz=8 (7 symbols)
			{21442, 23288, 24758, 26246, 27649, 28980, 30563, 0, 0}, {5863, 14933, 17552, 20668, 23683, 26411, 29273, 0, 0}, {3415, 25810, 26877, 27990, 29223, 30394, 31618, 0, 0}, {17965, 20084, 22232, 23974, 26274, 28402, 30390, 0, 0}, {31190, 31329, 31516, 31679, 31825, 32026, 32322, 0, 0},
		},
	},
}

// defaultCflSignCDF contains default CFL joint sign CDF.
// AV1 spec Section 9.4. 8 symbols.
var defaultCflSignCDF = []uint16{1418, 2123, 13340, 18405, 26972, 28343, 32294, 0, 0}

// defaultCflAlphaCDF contains default CFL alpha CDFs.
// AV1 spec Section 9.4. [ctx], 16 symbols.
var defaultCflAlphaCDF = [6][]uint16{
	0: {7637, 20719, 31401, 32481, 32657, 32688, 32692, 32696, 32700, 32704, 32708, 32712, 32716, 32720, 32724, 0, 0},
	1: {14365, 23603, 28135, 31168, 32167, 32395, 32487, 32573, 32620, 32647, 32668, 32672, 32676, 32680, 32684, 0, 0},
	2: {11532, 22380, 28445, 31360, 32349, 32523, 32584, 32649, 32673, 32677, 32681, 32685, 32689, 32693, 32697, 0, 0},
	3: {26990, 31402, 32282, 32571, 32692, 32696, 32700, 32704, 32708, 32712, 32716, 32720, 32724, 32728, 32732, 0, 0},
	4: {17248, 26058, 28904, 30608, 31305, 31877, 32126, 32321, 32394, 32464, 32516, 32560, 32576, 32593, 32622, 0, 0},
	5: {14738, 21678, 25779, 27901, 29024, 30302, 30980, 31843, 32144, 32413, 32520, 32594, 32622, 32656, 32660, 0, 0},
}

// defaultSegmentIDCDF contains default spatial segment ID CDFs.
// AV1 spec Section 9.4. [ctx], 8 symbols.
var defaultSegmentIDCDF = [3][]uint16{
	0: []uint16{5622, 7893, 16093, 18233, 27809, 28373, 32533, 0, 0},
	1: []uint16{14274, 18230, 22557, 24935, 29980, 30851, 32344, 0, 0},
	2: []uint16{27527, 28487, 28723, 28890, 32397, 32647, 32679, 0, 0},
}

// defaultSegIDPredictedCDF contains default segment prediction CDFs.
// AV1 spec Section 9.4. [ctx], 2 symbols.
var defaultSegIDPredictedCDF = [3][]uint16{
	0: []uint16{16384, 0, 0},
	1: []uint16{16384, 0, 0},
	2: []uint16{16384, 0, 0},
}

// defaultRestorationTypeCDF contains default restoration type CDF. 3 symbols.
var defaultRestorationTypeCDF = []uint16{9413, 22581, 0, 0}

// defaultUseWienerCDF contains default use Wiener CDF. 2 symbols.
var defaultUseWienerCDF = []uint16{11570, 0, 0}

// defaultUseSGRProjCDF contains default use SGR CDF. 2 symbols.
var defaultUseSGRProjCDF = []uint16{16855, 0, 0}

// defaultWedgeIndexCDF contains default wedge index CDFs.
// AV1 spec Section 9.4. [bsize], 16 symbols.
var defaultWedgeIndexCDF = [22][]uint16{
	0: []uint16{2048, 4096, 6144, 8192, 10240, 12288, 14336, 16384, 18432, 20480, 22528, 24576, 26624, 28672, 30720, 0, 0},
	1: []uint16{2048, 4096, 6144, 8192, 10240, 12288, 14336, 16384, 18432, 20480, 22528, 24576, 26624, 28672, 30720, 0, 0},
	2: []uint16{2048, 4096, 6144, 8192, 10240, 12288, 14336, 16384, 18432, 20480, 22528, 24576, 26624, 28672, 30720, 0, 0},
	3: []uint16{2438, 4440, 6599, 8663, 11005, 12874, 15751, 18094, 20359, 22362, 24127, 25702, 27752, 29450, 31171, 0, 0},
	4: []uint16{806, 3266, 6005, 6738, 7218, 7367, 7771, 14588, 16323, 17367, 18452, 19422, 22839, 26127, 29629, 0, 0},
	5: []uint16{2779, 3738, 4683, 7213, 7775, 8017, 8655, 14357, 17939, 21332, 24520, 27470, 29456, 30529, 31656, 0, 0},
	6: []uint16{1684, 3625, 5675, 7108, 9302, 11274, 14429, 17144, 19163, 20961, 22884, 24471, 26719, 28714, 30877, 0, 0},
	7: []uint16{1142, 3491, 6277, 7314, 8089, 8355, 9023, 13624, 15369, 16730, 18114, 19313, 22521, 26012, 29550, 0, 0},
	8: []uint16{2742, 4195, 5727, 8035, 8980, 9336, 10146, 14124, 17270, 20533, 23434, 25972, 27944, 29570, 31416, 0, 0},
	9: []uint16{1727, 3948, 6101, 7796, 9841, 12344, 15766, 18944, 20638, 22038, 23963, 25311, 26988, 28766, 31012, 0, 0},
	10: []uint16{2048, 4096, 6144, 8192, 10240, 12288, 14336, 16384, 18432, 20480, 22528, 24576, 26624, 28672, 30720, 0, 0},
	11: []uint16{2048, 4096, 6144, 8192, 10240, 12288, 14336, 16384, 18432, 20480, 22528, 24576, 26624, 28672, 30720, 0, 0},
	12: []uint16{2048, 4096, 6144, 8192, 10240, 12288, 14336, 16384, 18432, 20480, 22528, 24576, 26624, 28672, 30720, 0, 0},
	13: []uint16{2048, 4096, 6144, 8192, 10240, 12288, 14336, 16384, 18432, 20480, 22528, 24576, 26624, 28672, 30720, 0, 0},
	14: []uint16{2048, 4096, 6144, 8192, 10240, 12288, 14336, 16384, 18432, 20480, 22528, 24576, 26624, 28672, 30720, 0, 0},
	15: []uint16{2048, 4096, 6144, 8192, 10240, 12288, 14336, 16384, 18432, 20480, 22528, 24576, 26624, 28672, 30720, 0, 0},
	16: []uint16{2048, 4096, 6144, 8192, 10240, 12288, 14336, 16384, 18432, 20480, 22528, 24576, 26624, 28672, 30720, 0, 0},
	17: []uint16{2048, 4096, 6144, 8192, 10240, 12288, 14336, 16384, 18432, 20480, 22528, 24576, 26624, 28672, 30720, 0, 0},
	18: []uint16{154, 987, 1925, 2051, 2088, 2111, 2151, 23033, 23703, 24284, 24985, 25684, 27259, 28883, 30911, 0, 0},
	19: []uint16{1135, 1322, 1493, 2635, 2696, 2737, 2770, 21016, 22935, 25057, 27251, 29173, 30089, 30960, 31933, 0, 0},
	20: []uint16{2048, 4096, 6144, 8192, 10240, 12288, 14336, 16384, 18432, 20480, 22528, 24576, 26624, 28672, 30720, 0, 0},
	21: []uint16{2048, 4096, 6144, 8192, 10240, 12288, 14336, 16384, 18432, 20480, 22528, 24576, 26624, 28672, 30720, 0, 0},
}

// defaultAllZeroCDF contains default all-zero (txb_skip) CDFs.
// AV1 spec Section 9.4. [txs_ctx][plane_type], 2 symbols.
// Values from dav1d default_coef_cdf[0].skip.
var defaultAllZeroCDF [13][2][]uint16

func init() {
	defaultAllZeroCDF[0][0] = []uint16{31849, 0, 0}
	defaultAllZeroCDF[0][1] = []uint16{31849, 0, 0}
	defaultAllZeroCDF[1][0] = []uint16{31548, 0, 0}
	defaultAllZeroCDF[1][1] = []uint16{31548, 0, 0}
	defaultAllZeroCDF[2][0] = []uint16{29957, 0, 0}
	defaultAllZeroCDF[2][1] = []uint16{29957, 0, 0}
	defaultAllZeroCDF[3][0] = []uint16{17920, 0, 0}
	defaultAllZeroCDF[3][1] = []uint16{17920, 0, 0}
	defaultAllZeroCDF[4][0] = []uint16{6308, 0, 0}
	defaultAllZeroCDF[4][1] = []uint16{6308, 0, 0}
	defaultAllZeroCDF[5][0] = []uint16{16384, 0, 0}
	defaultAllZeroCDF[5][1] = []uint16{16384, 0, 0}
	defaultAllZeroCDF[6][0] = []uint16{16384, 0, 0}
	defaultAllZeroCDF[6][1] = []uint16{16384, 0, 0}
	defaultAllZeroCDF[7][0] = []uint16{16384, 0, 0}
	defaultAllZeroCDF[7][1] = []uint16{16384, 0, 0}
	defaultAllZeroCDF[8][0] = []uint16{16384, 0, 0}
	defaultAllZeroCDF[8][1] = []uint16{16384, 0, 0}
	defaultAllZeroCDF[9][0] = []uint16{16384, 0, 0}
	defaultAllZeroCDF[9][1] = []uint16{16384, 0, 0}
	defaultAllZeroCDF[10][0] = []uint16{16384, 0, 0}
	defaultAllZeroCDF[10][1] = []uint16{16384, 0, 0}
	defaultAllZeroCDF[11][0] = []uint16{16384, 0, 0}
	defaultAllZeroCDF[11][1] = []uint16{16384, 0, 0}
	defaultAllZeroCDF[12][0] = []uint16{16384, 0, 0}
	defaultAllZeroCDF[12][1] = []uint16{16384, 0, 0}
}

// defaultEobPt16CDF contains default EOB position CDFs for 4x4 TX.
// AV1 spec Section 9.4. [plane_type], 5 symbols. Q_CTX=0, ctx=0 (square TX).
// In libaom: [PLANE_TYPES][2] — index [planeType][0] for square transforms.
var defaultEobPt16CDF = [2][]uint16{
	0: {840, 1039, 1980, 4895, 0, 0},
	1: {370, 671, 1883, 4471, 0, 0},
}

// defaultEobPt32CDF contains default EOB position CDFs for 8x8 TX.
// Q_CTX=0. [planeType] — original layout.
var defaultEobPt32CDF = [2][]uint16{
	0: {400, 520, 977, 2102, 6542, 0, 0},
	1: {210, 405, 1315, 3326, 7537, 0, 0},
}

// defaultEobPt64CDF contains default EOB position CDFs for 16x16 TX.
var defaultEobPt64CDF = [2][]uint16{
	0: {329, 498, 1101, 1784, 3265, 7758, 0, 0},
	1: {335, 730, 1459, 5494, 8755, 12997, 0, 0},
}

// defaultEobPt128CDF contains default EOB position CDFs for 32x32 TX.
var defaultEobPt128CDF = [2][]uint16{
	0: {219, 482, 1140, 2091, 3680, 6028, 12586, 0, 0},
	1: {371, 699, 1254, 4830, 9479, 12562, 17497, 0, 0},
}

// defaultEobPt256CDF contains default EOB position CDFs for 16x32/32x16 TX.
var defaultEobPt256CDF = [2][]uint16{
	0: {310, 584, 1887, 3589, 6168, 8611, 11352, 15652, 0, 0},
	1: {998, 1850, 2998, 5604, 17341, 19888, 22899, 25583, 0, 0},
}

// defaultEobPt512CDF contains default EOB position CDFs for 32x64/64x32 TX.
var defaultEobPt512CDF = [2][]uint16{
	0: {641, 983, 3707, 5430, 10234, 14958, 18788, 23412, 26061, 0, 0},
	1: {5095, 6446, 9996, 13354, 16017, 17986, 20919, 26129, 29140, 0, 0},
}

// defaultEobPt1024CDF contains default EOB position CDFs for 64x64 TX.
var defaultEobPt1024CDF = [2][]uint16{
	0: {393, 421, 751, 1623, 3160, 6352, 13345, 18047, 22571, 25830, 0, 0},
	1: {1865, 1988, 2930, 4242, 10533, 16538, 21354, 27255, 28546, 31784, 0, 0},
}

// defaultEobExtraCDF contains default EOB extra bit CDFs.
// AV1 spec Section 9.4. Indexed as [txSzCtx][planeType][eobPt-2], 2 symbols.
// txSzCtx: 0=TX_4X4, 1=TX_8X8, 2=TX_16X16, 3=TX_32X32, 4=TX_64X64.
// eobPt-2: 0..8 for the 9 eobPt values (2..10) that have extra bits.
// Values from libaom av1_default_eob_extra_cdfs, Q_CTX=0.
var defaultEobExtraCDF = [5][2][9][]uint16{
	// txSzCtx=0 (TX_4X4)
	0: {
		{
			{16961, 0, 0}, {17223, 0, 0}, {7621, 0, 0},
			{16384, 0, 0}, {16384, 0, 0}, {16384, 0, 0},
			{16384, 0, 0}, {16384, 0, 0}, {16384, 0, 0},
		},
		{
			{19069, 0, 0}, {22525, 0, 0}, {13377, 0, 0},
			{16384, 0, 0}, {16384, 0, 0}, {16384, 0, 0},
			{16384, 0, 0}, {16384, 0, 0}, {16384, 0, 0},
		},
	},
	// txSzCtx=1 (TX_8X8)
	1: {
		{
			{20401, 0, 0}, {17025, 0, 0}, {12845, 0, 0},
			{12873, 0, 0}, {14094, 0, 0}, {16384, 0, 0},
			{16384, 0, 0}, {16384, 0, 0}, {16384, 0, 0},
		},
		{
			{20681, 0, 0}, {20701, 0, 0}, {15250, 0, 0},
			{15017, 0, 0}, {14928, 0, 0}, {16384, 0, 0},
			{16384, 0, 0}, {16384, 0, 0}, {16384, 0, 0},
		},
	},
	// txSzCtx=2 (TX_16X16)
	2: {
		{
			{23905, 0, 0}, {17194, 0, 0}, {16170, 0, 0},
			{17695, 0, 0}, {13826, 0, 0}, {15810, 0, 0},
			{12036, 0, 0}, {16384, 0, 0}, {16384, 0, 0},
		},
		{
			{23959, 0, 0}, {20799, 0, 0}, {19021, 0, 0},
			{16203, 0, 0}, {17886, 0, 0}, {14144, 0, 0},
			{12010, 0, 0}, {16384, 0, 0}, {16384, 0, 0},
		},
	},
	// txSzCtx=3 (TX_32X32)
	3: {
		{
			{27399, 0, 0}, {16327, 0, 0}, {18071, 0, 0},
			{19584, 0, 0}, {20721, 0, 0}, {18432, 0, 0},
			{19560, 0, 0}, {10150, 0, 0}, {8805, 0, 0},
		},
		{
			{24932, 0, 0}, {20833, 0, 0}, {12027, 0, 0},
			{16670, 0, 0}, {19914, 0, 0}, {15106, 0, 0},
			{17662, 0, 0}, {13783, 0, 0}, {28756, 0, 0},
		},
	},
	// txSzCtx=4 (TX_64X64)
	4: {
		{
			{23406, 0, 0}, {21845, 0, 0}, {18432, 0, 0},
			{16384, 0, 0}, {17096, 0, 0}, {12561, 0, 0},
			{17320, 0, 0}, {22395, 0, 0}, {21370, 0, 0},
		},
		{
			{16384, 0, 0}, {16384, 0, 0}, {16384, 0, 0},
			{16384, 0, 0}, {16384, 0, 0}, {16384, 0, 0},
			{16384, 0, 0}, {16384, 0, 0}, {16384, 0, 0},
		},
	},
}

// defaultCoeffBaseEobCDF contains default coefficient base EOB CDFs.
// AV1 spec Section 9.4. [coeff_ctx][plane_type], 3 symbols.
// 4 contexts: c==0, c in [1,3], c in [4,9], c >= 10.
var defaultCoeffBaseEobCDF = [4][2][]uint16{
	0: {
		[]uint16{17837, 29055, 0, 0},
		[]uint16{21365, 30026, 0, 0},
	},
	1: {
		[]uint16{29600, 31446, 0, 0},
		[]uint16{30512, 32423, 0, 0},
	},
	2: {
		[]uint16{30844, 31878, 0, 0},
		[]uint16{31658, 32621, 0, 0},
	},
	3: {
		// Context 3 (c >= 10): high coefficients are very likely non-zero.
		// Values derived from libaom av1_default_coeff_base_eob_multi_cdfs.
		[]uint16{31499, 32296, 0, 0},
		[]uint16{32038, 32635, 0, 0},
	},
}

// defaultDcSignCDF contains default DC sign CDFs.
// AV1 spec Section 9.4. [plane_type][dc_sign_ctx], 2 symbols.
var defaultDcSignCDF = [2][3][]uint16{
	0: {
		[]uint16{16000, 0, 0},
		[]uint16{13056, 0, 0},
		[]uint16{18816, 0, 0},
	},
	1: {
		[]uint16{15232, 0, 0},
		[]uint16{12928, 0, 0},
		[]uint16{17280, 0, 0},
	},
}

// defaultCoeffBaseCDF contains default coefficient base CDFs.
// AV1 spec Section 9.4. [coeff_ctx][plane_type], 4 symbols.
// Values from dav1d default_coef_cdf[0].base_tok (tx_size=0).
var defaultCoeffBaseCDF [42][2][]uint16

func init() {
	defaultCoeffBaseCDF[0][0] = []uint16{4034, 8930, 12727, 0, 0}
	defaultCoeffBaseCDF[1][0] = []uint16{18082, 29741, 31877, 0, 0}
	defaultCoeffBaseCDF[2][0] = []uint16{12596, 26124, 30493, 0, 0}
	defaultCoeffBaseCDF[3][0] = []uint16{9446, 21118, 27005, 0, 0}
	defaultCoeffBaseCDF[4][0] = []uint16{6308, 15141, 21279, 0, 0}
	defaultCoeffBaseCDF[5][0] = []uint16{2463, 6357, 9783, 0, 0}
	defaultCoeffBaseCDF[6][0] = []uint16{20667, 30546, 31929, 0, 0}
	defaultCoeffBaseCDF[7][0] = []uint16{13043, 26123, 30134, 0, 0}
	defaultCoeffBaseCDF[8][0] = []uint16{8151, 18757, 24778, 0, 0}
	defaultCoeffBaseCDF[9][0] = []uint16{5255, 12839, 18632, 0, 0}
	defaultCoeffBaseCDF[10][0] = []uint16{2820, 7206, 11161, 0, 0}
	defaultCoeffBaseCDF[11][0] = []uint16{8192, 16384, 24576, 0, 0}
	defaultCoeffBaseCDF[12][0] = []uint16{8192, 16384, 24576, 0, 0}
	defaultCoeffBaseCDF[13][0] = []uint16{8192, 16384, 24576, 0, 0}
	defaultCoeffBaseCDF[14][0] = []uint16{8192, 16384, 24576, 0, 0}
	defaultCoeffBaseCDF[15][0] = []uint16{8192, 16384, 24576, 0, 0}
	defaultCoeffBaseCDF[16][0] = []uint16{8192, 16384, 24576, 0, 0}
	defaultCoeffBaseCDF[17][0] = []uint16{8192, 16384, 24576, 0, 0}
	defaultCoeffBaseCDF[18][0] = []uint16{8192, 16384, 24576, 0, 0}
	defaultCoeffBaseCDF[19][0] = []uint16{8192, 16384, 24576, 0, 0}
	defaultCoeffBaseCDF[20][0] = []uint16{8192, 16384, 24576, 0, 0}
	defaultCoeffBaseCDF[21][0] = []uint16{15736, 27553, 30604, 0, 0}
	defaultCoeffBaseCDF[22][0] = []uint16{11210, 23794, 28787, 0, 0}
	defaultCoeffBaseCDF[23][0] = []uint16{5947, 13874, 19701, 0, 0}
	defaultCoeffBaseCDF[24][0] = []uint16{4215, 9323, 13891, 0, 0}
	defaultCoeffBaseCDF[25][0] = []uint16{2833, 6462, 10059, 0, 0}
	defaultCoeffBaseCDF[26][0] = []uint16{19605, 30393, 31582, 0, 0}
	defaultCoeffBaseCDF[27][0] = []uint16{13523, 26252, 30248, 0, 0}
	defaultCoeffBaseCDF[28][0] = []uint16{8446, 18622, 24512, 0, 0}
	defaultCoeffBaseCDF[29][0] = []uint16{3818, 10343, 15974, 0, 0}
	defaultCoeffBaseCDF[30][0] = []uint16{1481, 4117, 6796, 0, 0}
	defaultCoeffBaseCDF[31][0] = []uint16{22649, 31302, 32190, 0, 0}
	defaultCoeffBaseCDF[32][0] = []uint16{14829, 27127, 30449, 0, 0}
	defaultCoeffBaseCDF[33][0] = []uint16{8313, 17702, 23304, 0, 0}
	defaultCoeffBaseCDF[34][0] = []uint16{3022, 8301, 12786, 0, 0}
	defaultCoeffBaseCDF[35][0] = []uint16{1536, 4412, 7184, 0, 0}
	defaultCoeffBaseCDF[36][0] = []uint16{22354, 29774, 31372, 0, 0}
	defaultCoeffBaseCDF[37][0] = []uint16{14723, 25472, 29214, 0, 0}
	defaultCoeffBaseCDF[38][0] = []uint16{6673, 13745, 18662, 0, 0}
	defaultCoeffBaseCDF[39][0] = []uint16{2068, 5766, 9322, 0, 0}
	defaultCoeffBaseCDF[40][0] = []uint16{8192, 16384, 24576, 0, 0}
	defaultCoeffBaseCDF[41][0] = []uint16{8192, 16384, 24576, 0, 0}
	defaultCoeffBaseCDF[0][1] = []uint16{6302, 16444, 21761, 0, 0}
	defaultCoeffBaseCDF[1][1] = []uint16{23040, 31538, 32475, 0, 0}
	defaultCoeffBaseCDF[2][1] = []uint16{15196, 28452, 31496, 0, 0}
	defaultCoeffBaseCDF[3][1] = []uint16{10020, 22946, 28514, 0, 0}
	defaultCoeffBaseCDF[4][1] = []uint16{6533, 16862, 23501, 0, 0}
	defaultCoeffBaseCDF[5][1] = []uint16{3538, 9816, 15076, 0, 0}
	defaultCoeffBaseCDF[6][1] = []uint16{24444, 31875, 32525, 0, 0}
	defaultCoeffBaseCDF[7][1] = []uint16{15881, 28924, 31635, 0, 0}
	defaultCoeffBaseCDF[8][1] = []uint16{9922, 22873, 28466, 0, 0}
	defaultCoeffBaseCDF[9][1] = []uint16{6527, 16966, 23691, 0, 0}
	defaultCoeffBaseCDF[10][1] = []uint16{4114, 11303, 17220, 0, 0}
	defaultCoeffBaseCDF[11][1] = []uint16{8192, 16384, 24576, 0, 0}
	defaultCoeffBaseCDF[12][1] = []uint16{8192, 16384, 24576, 0, 0}
	defaultCoeffBaseCDF[13][1] = []uint16{8192, 16384, 24576, 0, 0}
	defaultCoeffBaseCDF[14][1] = []uint16{8192, 16384, 24576, 0, 0}
	defaultCoeffBaseCDF[15][1] = []uint16{8192, 16384, 24576, 0, 0}
	defaultCoeffBaseCDF[16][1] = []uint16{8192, 16384, 24576, 0, 0}
	defaultCoeffBaseCDF[17][1] = []uint16{8192, 16384, 24576, 0, 0}
	defaultCoeffBaseCDF[18][1] = []uint16{8192, 16384, 24576, 0, 0}
	defaultCoeffBaseCDF[19][1] = []uint16{8192, 16384, 24576, 0, 0}
	defaultCoeffBaseCDF[20][1] = []uint16{8192, 16384, 24576, 0, 0}
	defaultCoeffBaseCDF[21][1] = []uint16{20201, 30770, 32209, 0, 0}
	defaultCoeffBaseCDF[22][1] = []uint16{14754, 28071, 31258, 0, 0}
	defaultCoeffBaseCDF[23][1] = []uint16{8378, 20186, 26517, 0, 0}
	defaultCoeffBaseCDF[24][1] = []uint16{5916, 15299, 21978, 0, 0}
	defaultCoeffBaseCDF[25][1] = []uint16{4268, 11583, 17901, 0, 0}
	defaultCoeffBaseCDF[26][1] = []uint16{24361, 32025, 32581, 0, 0}
	defaultCoeffBaseCDF[27][1] = []uint16{18673, 30105, 31943, 0, 0}
	defaultCoeffBaseCDF[28][1] = []uint16{10196, 22244, 27576, 0, 0}
	defaultCoeffBaseCDF[29][1] = []uint16{5495, 14349, 20417, 0, 0}
	defaultCoeffBaseCDF[30][1] = []uint16{2676, 7415, 11498, 0, 0}
	defaultCoeffBaseCDF[31][1] = []uint16{24678, 31958, 32585, 0, 0}
	defaultCoeffBaseCDF[32][1] = []uint16{18629, 29906, 31831, 0, 0}
	defaultCoeffBaseCDF[33][1] = []uint16{9364, 20724, 26315, 0, 0}
	defaultCoeffBaseCDF[34][1] = []uint16{4641, 12318, 18094, 0, 0}
	defaultCoeffBaseCDF[35][1] = []uint16{2758, 7387, 11579, 0, 0}
	defaultCoeffBaseCDF[36][1] = []uint16{25433, 31842, 32469, 0, 0}
	defaultCoeffBaseCDF[37][1] = []uint16{18795, 29289, 31411, 0, 0}
	defaultCoeffBaseCDF[38][1] = []uint16{7644, 17584, 23592, 0, 0}
	defaultCoeffBaseCDF[39][1] = []uint16{3408, 9014, 15047, 0, 0}
	defaultCoeffBaseCDF[40][1] = []uint16{8192, 16384, 24576, 0, 0}
	defaultCoeffBaseCDF[41][1] = []uint16{8192, 16384, 24576, 0, 0}
}

// defaultCoeffBRCDF contains default coefficient base range CDFs.
// AV1 spec Section 9.4. [br_ctx][plane_type], 4 symbols.
// Values from dav1d default_coef_cdf[0].br_tok (level=0).
var defaultCoeffBRCDF [21][2][]uint16

func init() {
	defaultCoeffBRCDF[0][0] = []uint16{14298, 20718, 24174, 0, 0}
	defaultCoeffBRCDF[1][0] = []uint16{12536, 19601, 23789, 0, 0}
	defaultCoeffBRCDF[2][0] = []uint16{8712, 15051, 19503, 0, 0}
	defaultCoeffBRCDF[3][0] = []uint16{6170, 11327, 15434, 0, 0}
	defaultCoeffBRCDF[4][0] = []uint16{4742, 8926, 12538, 0, 0}
	defaultCoeffBRCDF[5][0] = []uint16{3803, 7317, 10546, 0, 0}
	defaultCoeffBRCDF[6][0] = []uint16{1696, 3317, 4871, 0, 0}
	defaultCoeffBRCDF[7][0] = []uint16{14392, 19951, 22756, 0, 0}
	defaultCoeffBRCDF[8][0] = []uint16{15978, 23218, 26818, 0, 0}
	defaultCoeffBRCDF[9][0] = []uint16{12187, 19474, 23889, 0, 0}
	defaultCoeffBRCDF[10][0] = []uint16{9176, 15640, 20259, 0, 0}
	defaultCoeffBRCDF[11][0] = []uint16{7068, 12655, 17028, 0, 0}
	defaultCoeffBRCDF[12][0] = []uint16{5656, 10442, 14472, 0, 0}
	defaultCoeffBRCDF[13][0] = []uint16{2580, 4992, 7244, 0, 0}
	defaultCoeffBRCDF[14][0] = []uint16{12136, 18049, 21426, 0, 0}
	defaultCoeffBRCDF[15][0] = []uint16{13784, 20721, 24481, 0, 0}
	defaultCoeffBRCDF[16][0] = []uint16{10836, 17621, 21900, 0, 0}
	defaultCoeffBRCDF[17][0] = []uint16{8372, 14444, 18847, 0, 0}
	defaultCoeffBRCDF[18][0] = []uint16{6523, 11779, 16000, 0, 0}
	defaultCoeffBRCDF[19][0] = []uint16{5337, 9898, 13760, 0, 0}
	defaultCoeffBRCDF[20][0] = []uint16{3034, 5860, 8462, 0, 0}
	defaultCoeffBRCDF[0][1] = []uint16{15967, 22905, 26286, 0, 0}
	defaultCoeffBRCDF[1][1] = []uint16{13534, 20654, 24579, 0, 0}
	defaultCoeffBRCDF[2][1] = []uint16{9504, 16092, 20535, 0, 0}
	defaultCoeffBRCDF[3][1] = []uint16{6975, 12568, 16903, 0, 0}
	defaultCoeffBRCDF[4][1] = []uint16{5364, 10091, 14020, 0, 0}
	defaultCoeffBRCDF[5][1] = []uint16{4357, 8370, 11857, 0, 0}
	defaultCoeffBRCDF[6][1] = []uint16{2506, 4934, 7218, 0, 0}
	defaultCoeffBRCDF[7][1] = []uint16{23032, 28815, 30936, 0, 0}
	defaultCoeffBRCDF[8][1] = []uint16{19540, 26704, 29719, 0, 0}
	defaultCoeffBRCDF[9][1] = []uint16{15158, 22969, 27097, 0, 0}
	defaultCoeffBRCDF[10][1] = []uint16{11408, 18865, 23650, 0, 0}
	defaultCoeffBRCDF[11][1] = []uint16{8885, 15448, 20250, 0, 0}
	defaultCoeffBRCDF[12][1] = []uint16{7108, 12853, 17416, 0, 0}
	defaultCoeffBRCDF[13][1] = []uint16{4231, 8041, 11480, 0, 0}
	defaultCoeffBRCDF[14][1] = []uint16{19823, 26490, 29156, 0, 0}
	defaultCoeffBRCDF[15][1] = []uint16{18890, 25929, 28932, 0, 0}
	defaultCoeffBRCDF[16][1] = []uint16{15660, 23491, 27433, 0, 0}
	defaultCoeffBRCDF[17][1] = []uint16{12147, 19776, 24488, 0, 0}
	defaultCoeffBRCDF[18][1] = []uint16{9728, 16774, 21649, 0, 0}
	defaultCoeffBRCDF[19][1] = []uint16{7919, 14277, 19066, 0, 0}
	defaultCoeffBRCDF[20][1] = []uint16{5440, 10170, 14185, 0, 0}
}


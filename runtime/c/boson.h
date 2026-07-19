/* boson.h — the layout/ABI contract shared by Boson→C emitted code and the
 * hand-written C runtime floor. See docs/proposed/PROPOSAL_c_transpiler.md.
 *
 * Phase 1: integer scalar typedefs and the wrapping-arithmetic helpers.
 * Aggregate representations (slices, interfaces, ADTs) arrive in later phases;
 * this header will become partly *generated* then (one slice struct per
 * element type). For now it is static.
 */
#ifndef BOSON_H
#define BOSON_H

#include <stdint.h>

typedef int8_t   bo_i8;
typedef int16_t  bo_i16;
typedef int32_t  bo_i32;
typedef int64_t  bo_i64;
typedef uint8_t  bo_u8;
typedef uint8_t  bo_byte;
typedef uint16_t bo_u16;
typedef uint32_t bo_u32;
typedef uint64_t bo_u64;
typedef _Bool    bo_bool;

/* Integer overflow wraps (two's complement), per the language decision
 * (DESIGN.md §Expressions). Signed +,-,* are computed in the unsigned domain
 * so the emitted C is well-formed — no signed-overflow UB, so the optimizer
 * can go full -O2/-O3. The narrowing back to signed is implementation-defined
 * two's complement on every real target (mandated by C23). Unsigned wrap is
 * already modular-defined, so the emitter uses bare operators there and needs
 * no helper. */
static inline bo_i8  bo_add_i8 (bo_i8  a, bo_i8  b) { return (bo_i8 )((bo_u8 )a + (bo_u8 )b); }
static inline bo_i8  bo_sub_i8 (bo_i8  a, bo_i8  b) { return (bo_i8 )((bo_u8 )a - (bo_u8 )b); }
static inline bo_i8  bo_mul_i8 (bo_i8  a, bo_i8  b) { return (bo_i8 )((bo_u8 )a * (bo_u8 )b); }

static inline bo_i16 bo_add_i16(bo_i16 a, bo_i16 b) { return (bo_i16)((bo_u16)a + (bo_u16)b); }
static inline bo_i16 bo_sub_i16(bo_i16 a, bo_i16 b) { return (bo_i16)((bo_u16)a - (bo_u16)b); }
static inline bo_i16 bo_mul_i16(bo_i16 a, bo_i16 b) { return (bo_i16)((bo_u16)a * (bo_u16)b); }

static inline bo_i32 bo_add_i32(bo_i32 a, bo_i32 b) { return (bo_i32)((bo_u32)a + (bo_u32)b); }
static inline bo_i32 bo_sub_i32(bo_i32 a, bo_i32 b) { return (bo_i32)((bo_u32)a - (bo_u32)b); }
static inline bo_i32 bo_mul_i32(bo_i32 a, bo_i32 b) { return (bo_i32)((bo_u32)a * (bo_u32)b); }

static inline bo_i64 bo_add_i64(bo_i64 a, bo_i64 b) { return (bo_i64)((bo_u64)a + (bo_u64)b); }
static inline bo_i64 bo_sub_i64(bo_i64 a, bo_i64 b) { return (bo_i64)((bo_u64)a - (bo_u64)b); }
static inline bo_i64 bo_mul_i64(bo_i64 a, bo_i64 b) { return (bo_i64)((bo_u64)a * (bo_u64)b); }

#endif /* BOSON_H */

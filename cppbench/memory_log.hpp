// Memory-only log adapter for the production BasicMemTable. The MemTable still
// packs/copies records and maintains its ART; only durable log I/O is replaced.
// Buffers outlive all tree views. There is no reclamation in this benchmark.
#pragma once

#include <turtle_kv/mem_table/mem_table.ipp>
#include <memory>
#include <vector>

namespace turtle_kv {
struct BenchmarkMemoryLog {
  struct Writer {};
  struct BlockBuffer {
    std::vector<char> bytes;
    usize used = 0, slots = 0;
    i32 refs = 0;
    explicit BlockBuffer(usize size) : bytes(size) {}
    usize block_size() const { return bytes.size(); }
    usize slot_count() const { return slots; }
    i32 ref_count() const { return refs; }
    void add_ref(i32 delta) { refs += delta; }
    void remove_ref(i32 delta) { refs -= delta; BATT_CHECK_GE(refs, 0); }
  };
  struct WriterContext {
    using BlockBuffer = BenchmarkMemoryLog::BlockBuffer;
    std::vector<std::unique_ptr<BlockBuffer>> blocks;
    BlockBuffer* current = nullptr;
    i64 next_offset = 0;

    template <typename Fn>
    Status append_slot(EditOffset lower, usize size, batt::WaitForResource, Fn&& callback) {
      BATT_CHECK_GE(next_offset, lower.value());
      if (!current || current->bytes.size() - current->used < size) {
        blocks.emplace_back(std::make_unique<BlockBuffer>(std::max<usize>(65536, size)));
        current = blocks.back().get();
      }
      callback(FirstVisitToBlock{current->slots == 0}, current,
               MutableBuffer{current->bytes.data() + current->used, size},
               EditOffset{next_offset});
      current->used += size;
      current->slots++;
      next_offset += size;
      return OkStatus();
    }
  };
};
static_assert(MemTableStorage<BenchmarkMemoryLog>);
using BenchmarkMemTable = BasicMemTable<BenchmarkMemoryLog, MemTablePageCacheAllocationTracker>;
} // namespace turtle_kv

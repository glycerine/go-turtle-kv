// In-memory C++ TurtleTree baseline. Tree-core mode borrows input bytes;
// optional memory-log mode includes the production MemTable and owned copies.
// Durable WAL I/O and checkpoint serialization are outside timed operations.
#include <turtle_kv/tree/in_memory_leaf.hpp>
#include <turtle_kv/tree/in_memory_node.hpp>
#include <turtle_kv/tree/memory_storage.hpp>
#include <turtle_kv/tree/pinning_page_loader.hpp>
#include <turtle_kv/tree/subtree_table.hpp>
#include <batteries/async/runtime.hpp>
#include <turtle_kv/kv_store_scanner.hpp>
#include <llfs/appendable_job.hpp>
#include <numeric>
#include <algorithm>
#include <chrono>
#include <cstdint>
#include <iostream>
#include <string>
#include <vector>
#include "memory_log.hpp"

using namespace turtle_kv;
using Clock = std::chrono::steady_clock;
uint64_t mix(uint64_t x) {
 x += 0x9e3779b97f4a7c15ULL;
 x ^= x >> 30; x *= 0xbf58476d1ce4e5b9ULL;
 x ^= x >> 27; x *= 0x94d049bb133111ebULL;
 return x ^ (x >> 31);
}
uint64_t bench_key(uint64_t x) {
 const uint64_t mask=(1ULL<<63)-1;
 x &= mask; x ^= x>>30; x=x*0x3f58476d1ce4e5b9ULL&mask;
 x ^= x>>27; x=x*0x14d049bb133111ebULL&mask;
 x ^= x>>31; return x<<1;
}
double ns(Clock::time_point start, size_t count) {
 return std::chrono::duration<double,std::nano>(Clock::now()-start).count()/count;
}
int main(int argc, char** argv) {
 const size_t n=argc>1?std::stoull(argv[1]):65536;
 const size_t count=argc>2?std::stoull(argv[2]):1000000;
 const size_t leaf_bytes=argc>3?std::stoull(argv[3]):32768;
 const size_t batch_records=argc>4?std::stoull(argv[4]):256;
 const size_t scan_count=argc>5?std::stoull(argv[5]):4096;
 const bool verify_checkpoint=argc>6?std::stoi(argv[6])!=0:false;
 const bool use_memtable=argc>7?std::stoi(argv[7])!=0:false;
 const int trace_bits=argc>8?std::stoi(argv[8]):16;
 const size_t memory_bytes=argc>9?std::stoull(argv[9]):256ULL<<20;
 const size_t value_bytes=argc>10?std::stoull(argv[10]):100;
 if(use_memtable && scan_count!=0) {
  std::cerr<<"memory-log MemTable mode currently supports point workloads only; set scan_count=0\n";return 2;
 }
 if (value_bytes<1 || n<2 || count<1 || leaf_bytes<4096 || (leaf_bytes&(leaf_bytes-1))!=0 || batch_records<1 || trace_bits<1 || trace_bits>24) return 2;
 auto options=TreeOptions::with_default_values().set_leaf_size(leaf_bytes).set_node_size(4096)
   .set_key_size_hint(8).set_value_size_hint(value_bytes);
 if(batch_records>options.flush_size()/(value_bytes+17)) {
  std::cerr<<"batch exceeds conservative leaf flush limit: "<<options.flush_size()/(value_bytes+17)<<std::endl;return 2;
 }
 std::cout<<"C++ config leaf_bytes="<<leaf_bytes<<" batch_records="<<batch_records
   <<" node_bytes="<<options.node_size()<<" flush_bytes="<<options.flush_size()
   <<" mode="<<(use_memtable?"MemTable+Tree+memory-log":"Tree-borrowed-inputs")
   <<" value_bytes="<<value_bytes<<" trace_bits="<<trace_bits<<" memory_device_bytes="<<memory_bytes<<std::endl;
 auto cache=make_memory_page_cache(batt::Runtime::instance().default_scheduler(),options,memory_bytes);
 PinningPageLoader loader{*cache};
 Subtree tree=Subtree::make_empty();
 SubtreeTable table{*cache,options,tree};
 BatchUpdateMetrics metrics;
 OvercommitMetrics overcommit_metrics;
 MemTableMetrics mem_metrics{&overcommit_metrics};
 MemTablePageCacheAllocationTracker allocation_tracker{*cache,overcommit_metrics};
 BenchmarkMemoryLog::WriterContext memory_log;
 std::unique_ptr<BenchmarkMemTable> mem;
 size_t pending_ops=0;
 auto new_mem=[&]() {
  // A new MemTable must attach every log block it first visits.
  memory_log.current=nullptr;
  mem=std::make_unique<BenchmarkMemTable>(allocation_tracker,mem_metrics,
      EditOffset{memory_log.next_offset},options.flush_size(),16);
 };
 auto apply_result=[&](MergeCompactor::ResultSet<false> result) {
  BatchUpdate batch{.context={.worker_pool=batt::WorkerPool::null_pool(),.page_loader=loader,
    .cancel_token=batt::CancelToken{},.metrics=metrics,.overcommit=llfs::PageCacheOvercommit::not_allowed()},
    .result_set=std::move(result),.edit_size_totals=None};
  batch.update_edit_size_totals();
  auto height=tree.get_height(loader,llfs::PageCacheOvercommit::not_allowed());
  if(!height.ok()) {std::cerr<<height.status()<<'\n';std::exit(1);}
  auto status=tree.apply_batch_update(options,ParentNodeHeight{*height+1},batch,global_max_key(),IsRoot{true});
  if(!status.ok()) {std::cerr<<status<<'\n';std::exit(1);}
 };
 auto flush_mem=[&]() {
  BATT_CHECK(mem->finalize([&]{return EditOffset{memory_log.next_offset};}));
  {
   BenchmarkMemTable::BatchCompactor compactor{*mem,options.flush_size()};
   while(compactor.has_next()) {
    auto result=compactor.consume_next();
    if(result.empty()) {std::cerr<<"MemTable compaction made no progress\n";std::exit(1);}
    apply_result(std::move(result));
   }
  }
  mem.reset();pending_ops=0;new_mem();
 };
 auto put=[&](KeyView key,ValueView v) {
  auto status=mem->put(memory_log,key,v);
  if(!status.ok()) {std::cerr<<"MemTable put: "<<status.status()<<'\n';std::exit(1);}
  if(++pending_ops==batch_records)flush_mem();
 };
 auto get=[&](KeyView key)->StatusOr<ValueView> {
  if(use_memtable) {
   auto v=mem->get(key);
   if(v)return *v;
  }
  return table.get(key);
 };
 std::vector<std::string> keys(n),misses(n);
 std::string value(value_bytes,'\0');for(size_t i=0;i<value.size();i++)value[i]=static_cast<char>(i);
 std::vector<size_t> trace(size_t{1}<<trace_bits);
 uint64_t trace_checksum=0;
 for(size_t i=0;i<trace.size();i++) {
  trace[i]=mix(i+2023)%n;trace_checksum=trace_checksum*0x9e3779b97f4a7c15ULL+trace[i];
 }
 std::cout<<"Trace checksum="<<std::hex<<trace_checksum<<std::dec<<std::endl;
 for(size_t i=0;i<n;i++) {
  uint64_t k=bench_key(i);keys[i].resize(8);
  for(size_t j=0;j<8;j++)keys[i][j]=static_cast<char>(k>>(56-8*j));
  misses[i]=keys[i];misses[i][7]|=1;
 }
 std::string update_value=value;update_value[0]=1;
 auto apply=[&](size_t start,size_t end,bool update) {
  if(use_memtable) {
   for(size_t i=start;i<end;i++) {
    const auto& key=keys[update?trace[i%trace.size()]:i];
    put(KeyView{key},ValueView::from_str(update?update_value:value));
   }
   return;
  }
  std::vector<EditView> edits;edits.reserve(end-start);
  for(size_t i=start;i<end;i++) {
   const auto& key=keys[update?trace[i%trace.size()]:i];
   edits.emplace_back(KeyView{key},ValueView::from_str(update?update_value:value));
  }
  std::sort(edits.begin(),edits.end(),[](const auto& a,const auto& b){return a.key<b.key;});
  // Repeated updates within a batch have the same value, so stable order is immaterial.
  edits.erase(std::unique(edits.begin(),edits.end(),[](const auto& a,const auto& b){return a.key==b.key;}),edits.end());
  MergeCompactor::ResultSet<false> result;result.append(std::move(edits));
  apply_result(std::move(result));
 };
 auto start=Clock::now();
 if(use_memtable)new_mem();
 for(size_t i=0;i<n;i+=batch_records)apply(i,std::min(n,i+batch_records),false);
 std::cout<<"C++/TreeInsert N="<<n<<" ns/key="<<ns(start,n)<<std::endl;
 // Validate all loaded keys before timing reads.
 for(size_t i=0;i<n;i++) {
  auto v=get(KeyView{keys[i]});
  if(!v.ok() || v->as_str()!=value) {std::cerr<<"load validation failed at "<<i<<'\n';return 1;}
 }
 uint64_t sum=0;start=Clock::now();
 for(size_t i=0;i<count;i++) {
  auto v=get(KeyView{keys[trace[i%trace.size()]]});
  if(!v.ok())return 1;
  sum+=static_cast<unsigned char>(v->as_str().back());
 }
 std::cout<<"C++/TreeHit N="<<n<<" ns/op="<<ns(start,count)<<" checksum="<<sum<<std::endl;
 start=Clock::now();
 for(size_t i=0;i<count;i++) {
  auto v=get(KeyView{misses[trace[i%trace.size()]]});
  if(v.ok() || v.status()!=batt::StatusCode::kNotFound)return 1;
 }
 std::cout<<"C++/TreeMiss N="<<n<<" ns/op="<<ns(start,count)<<std::endl;
 start=Clock::now();
 for(size_t i=0;i<count;i+=batch_records)apply(i,std::min(count,i+batch_records),true);
 std::cout<<"C++/TreeUpdate N="<<n<<" ns/op="<<ns(start,count)<<std::endl;
 for(size_t i=0;i<std::min(count,trace.size());i++) {
  auto v=get(KeyView{keys[trace[i]]});
  if(!v.ok() || v->as_str()!=update_value) {std::cerr<<"update validation failed"<<std::endl;return 1;}
 }
 if(use_memtable) {
  // Match Go's Mixed50 starting state: freshly populated, without the preceding
  // update-only phase. Reset outside timing, then apply alternating owned writes.
  mem.reset();tree=Subtree::make_empty();
  memory_log.current=nullptr;memory_log.blocks.clear();memory_log.next_offset=0;
  pending_ops=0;new_mem();
  for(size_t i=0;i<n;i+=batch_records)apply(i,std::min(n,i+batch_records),false);
  sum=0;start=Clock::now();
  for(size_t i=0;i<count;i++) {
   const auto key=KeyView{keys[trace[i%trace.size()]]};
   if((i&1)==0)put(key,ValueView::from_str(value));
   else {
    auto v=get(key);if(!v.ok())return 1;
    sum+=static_cast<unsigned char>(v->as_str().back());
   }
  }
  std::cout<<"C++/MemTreeMixed50 N="<<n<<" ns/op="<<ns(start,count)<<" checksum="<<sum<<std::endl;
  // Check every current value, including keys never hit by the hot trace.
  for(size_t i=0;i<n;i++) {
   auto v=get(KeyView{keys[i]});
   if(!v.ok() || v->as_str()!=value) {
    std::cerr<<"MemTable/tree final validation failed rank="<<i<<'\n';return 1;
   }
  }
  std::cout<<"MemTable/tree full validation passed N="<<n<<std::endl;
  // Grow from N to 2N with unique odd keys; include all automatic flushes.
  start=Clock::now();
  for(size_t i=0;i<n;i++)put(KeyView{misses[i]},ValueView::from_str(value));
  std::cout<<"C++/FreshPut N="<<n<<" ns/key="<<ns(start,n)<<std::endl;
  for(size_t i=0;i<n;i++) {
   for(const auto* key: {&keys[i],&misses[i]}) {
    auto v=get(KeyView{*key});
    if(!v.ok() || v->as_str()!=value) {std::cerr<<"growth validation failed"<<std::endl;return 1;}
   }
  }
  std::cout<<"Fresh growth validation passed N="<<n<<std::endl;
 }
 // The production C++ scanner accepts serialized checkpoint trees only.
 // Materialize into memory-backed page devices outside all scan timers.
 if(scan_count!=0) {
  auto status=KVStore::register_page_layouts(*cache);
  if(!status.ok()) {std::cerr<<status<<std::endl;return 1;}
  auto job=cache->new_job();
  TreeSerializeContext context{options,*job,batt::WorkerPool::null_pool(),
    llfs::PageCacheOvercommit::not_allowed(),FilterPageWriteState::make_new()};
  status=tree.start_serialize(context);
  if(!status.ok()) {std::cerr<<status<<std::endl;return 1;}
  status=context.build_all_pages();
  if(!status.ok()) {std::cerr<<status<<std::endl;return 1;}
  auto root=tree.finish_serialize(context);
  if(!root.ok()) {std::cerr<<root.status()<<std::endl;return 1;}
  job->new_root(*root);
  status=llfs::unsafe_commit_job(std::move(job));
  if(!status.ok()) {std::cerr<<status<<std::endl;return 1;}
  auto height=tree.get_height(loader,llfs::PageCacheOvercommit::not_allowed());
  if(!height.ok()) {std::cerr<<height.status()<<std::endl;return 1;}
  std::vector<bool> updated(n,false);
  for(size_t i=0;i<std::min(count,trace.size());i++)updated[trace[i]]=true;
  if(verify_checkpoint) {
   for(size_t i=0;i<n;i++) {
    auto v=table.get(KeyView{keys[i]});
    if(!v.ok() || v->as_str()!=(updated[i]?update_value:value)) {
     std::cerr<<"serialized lookup validation failed rank="<<i<<std::endl;return 1;
    }
   }
   std::cout<<"Serialized lookup validation passed N="<<n<<std::endl;
  }
  std::vector<size_t> sorted(n);std::iota(sorted.begin(),sorted.end(),0);
  std::sort(sorted.begin(),sorted.end(),[&](size_t a,size_t b){return keys[a]<keys[b];});
  std::vector<size_t> remaining(n);
  for(size_t i=0;i<n;i++)remaining[sorted[i]]=n-i;
  auto scan=[&](const std::string& key,size_t length,bool verify) {
   auto expected=sorted.end();
   if(verify)expected=std::lower_bound(sorted.begin(),sorted.end(),key,
      [&](size_t rank,const std::string& target){return keys[rank]<target;});
   KVStoreScanner scanner{loader,tree.page_id_slot_or_panic(),*height,
      KeyView{key},options.trie_index_sharded_view_size()};
   auto scan_status=scanner.start();
   if(!scan_status.ok()) {std::cerr<<scan_status<<std::endl;std::exit(1);}
   size_t visited=0;
   while(visited<length) {
    auto item=scanner.next();if(!item)break;
    if(verify) {
     if(expected==sorted.end() || item->key!=KeyView{keys[*expected]} ||
       item->value.as_str()!=(updated[*expected]?update_value:value)) {
      std::cerr<<"scan validation failed visited="<<visited<<" requested="<<length
        <<" next_expected_rank="<<(expected==sorted.end()?n:*expected)<<std::endl;std::exit(1);
     }
     ++expected;
    }
    sum+=static_cast<unsigned char>(item->value.as_str().back());visited++;
   }
   if(verify && visited<length && expected!=sorted.end()) {
    std::cerr<<"scan ended early requested="<<length<<" visited="<<visited
       <<" expected_remaining="<<std::distance(expected,sorted.end())
       <<" next_expected_rank="<<*expected<<" tree_height="<<*height<<std::endl;std::exit(1);
   }
   return visited;
  };
  if(verify_checkpoint) {
   scan(keys[sorted.front()],n,true);
   std::cout<<"Serialized full scan validation passed N="<<n<<std::endl;
  }
  for(size_t length: {size_t{1},size_t{100},size_t{1000}}) {
   for(size_t i=0;i<32;i++)scan(keys[trace[i]],length,true);
   scan(keys[sorted.back()],length,true);
   uint64_t expected_visited=0;
   for(size_t i=0;i<scan_count;i++)expected_visited+=std::min(length,remaining[trace[i%trace.size()]]);
   uint64_t visited=0;sum=0;start=Clock::now();
   for(size_t i=0;i<scan_count;i++)visited+=scan(keys[trace[i%trace.size()]],length,false);
   const double elapsed=ns(start,scan_count);
   if(visited!=expected_visited) {
    std::cerr<<"timed scan count mismatch length="<<length<<" visited="<<visited
      <<" expected="<<expected_visited<<std::endl;return 1;
   }
   std::cout<<"C++/CheckpointScan N="<<n<<" length="<<length
      <<" ns/scan="<<elapsed<<" visited="<<visited<<" checksum="<<sum<<std::endl;
  }
 }
 // Write/point timers above measure only the in-memory tree core.
 return 0;
}

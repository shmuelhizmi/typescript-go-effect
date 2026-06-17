// multiple forks stored as fibers and joined later (SPEC §8)

declare const task1: any, task2: any, task3: any;

effect xeConcForkJoinMulti() {
  f1 <- fork task1
  f2 <- fork task2
  f3 <- fork task3
  a <- join f1
  b <- join f2
  c <- join f3
  return [a, b, c]
}
